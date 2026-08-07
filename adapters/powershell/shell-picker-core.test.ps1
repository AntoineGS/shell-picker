Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:AssertionCount = 0

function Assert-True {
    param(
        [bool]$Condition,
        [string]$Message
    )

    if (-not $Condition) {
        throw [System.Exception]::new($Message)
    }

    [void]($script:AssertionCount++)
}

function Assert-False {
    param(
        [bool]$Condition,
        [string]$Message
    )

    Assert-True (-not $Condition) $Message
}

function Assert-Null {
    param(
        [AllowNull()]
        [object]$Value,
        [string]$Message
    )

    Assert-True ($null -eq $Value) $Message
}

function Assert-Equal {
    param(
        [AllowNull()]
        [object]$Expected,
        [AllowNull()]
        [object]$Actual,
        [string]$Message
    )

    $same = if (($Expected -is [string]) -or ($Actual -is [string])) {
        ([string]$Expected -ceq [string]$Actual)
    }
    else {
        ($Expected -ceq $Actual)
    }

    if (-not $same) {
        throw [System.Exception]::new("$Message (expected '$Expected', actual '$Actual')")
    }

    [void]($script:AssertionCount++)
}

function Assert-ExactType {
    param(
        [AllowNull()]
        [object]$Value,
        [type]$ExpectedType,
        [string]$Message
    )

    $actualType = if ($null -eq $Value) { $null } else { $Value.GetType() }
    if (($null -eq $actualType) -or ($actualType -ne $ExpectedType)) {
        $actualName = if ($null -eq $actualType) { '<null>' } else { $actualType.FullName }
        throw [System.Exception]::new("$Message (expected '$($ExpectedType.FullName)', actual '$actualName')")
    }

    [void]($script:AssertionCount++)
}

function Get-TestInputAst {
    param([string]$Source)

    $tokens = $null
    $parseErrors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseInput(
        $Source,
        [ref]$tokens,
        [ref]$parseErrors
    )
    if (($null -ne $parseErrors) -and ($parseErrors.Count -ne 0)) {
        throw [System.Exception]::new("sentinel source failed to parse: $($parseErrors[0].Message)")
    }

    return $ast
}

function Get-TestFileAst {
    param([string]$Path)

    $tokens = $null
    $parseErrors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile(
        $Path,
        [ref]$tokens,
        [ref]$parseErrors
    )
    if (($null -ne $parseErrors) -and ($parseErrors.Count -ne 0)) {
        throw [System.Exception]::new("core source failed to parse: $($parseErrors[0].Message)")
    }

    return $ast
}

function Assert-AstHasNoForbiddenReferences {
    param(
        [System.Management.Automation.Language.Ast]$Ast
    )

    $forbiddenCommandNames = @(
        'PSReadLine',
        'GetEnvironmentVariable',
        'SetEnvironmentVariable',
        'Start-Process',
        'Test-Path',
        'Get-Item',
        'Resolve-Path',
        'New-Item',
        'Set-Content',
        'Remove-Item',
        'Set-Location'
    )
    $forbiddenTypeNames = @(
        'PSReadLine',
        'Diagnostics.Process',
        'System.Diagnostics.Process',
        'ProcessStartInfo',
        'System.Diagnostics.ProcessStartInfo',
        'System.IO.File',
        'System.IO.Directory'
    )
    $forbiddenMemberNames = @(
        'GetEnvironmentVariable',
        'SetEnvironmentVariable'
    )

    $commandAsts = @($Ast.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.CommandAst]
    }, $true))
    foreach ($commandAst in $commandAsts) {
        $commandName = $commandAst.GetCommandName()
        if (($null -ne $commandName) -and ($forbiddenCommandNames -contains $commandName)) {
            throw [System.Exception]::new("core source contains forbidden command AST '$commandName'")
        }
    }

    $typeAsts = @($Ast.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.TypeExpressionAst]
    }, $true))
    foreach ($typeAst in $typeAsts) {
        $typeName = $typeAst.TypeName.FullName
        if (($null -ne $typeName) -and ($forbiddenTypeNames -contains $typeName)) {
            throw [System.Exception]::new("core source contains forbidden type AST '$typeName'")
        }
    }

    $memberAsts = @($Ast.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.InvokeMemberExpressionAst]
    }, $true))
    foreach ($memberAst in $memberAsts) {
        $memberName = if ($memberAst.Member -is [System.Management.Automation.Language.StringConstantExpressionAst]) {
            [string]$memberAst.Member.Value
        }
        else {
            $null
        }
        if (($null -ne $memberName) -and ($forbiddenMemberNames -contains $memberName)) {
            throw [System.Exception]::new("core source contains forbidden member AST '$memberName'")
        }
    }

    $variableAsts = @($Ast.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.VariableExpressionAst]
    }, $true))
    foreach ($variableAst in $variableAsts) {
        $variableName = $variableAst.VariablePath.UserPath
        if (($null -ne $variableName) -and $variableName.StartsWith('Env:', [System.StringComparison]::OrdinalIgnoreCase)) {
            throw [System.Exception]::new("core source contains forbidden Env: variable AST '$variableName'")
        }
    }
}

function Assert-AstRejectsForbiddenSource {
    param(
        [string]$Source,
        [string]$Message
    )

    $ast = Get-TestInputAst -Source $Source
    $rejected = $false
    try {
        Assert-AstHasNoForbiddenReferences -Ast $ast
    }
    catch {
        $rejected = $true
    }

    Assert-True $rejected $Message
}

function Assert-AstAllowsSource {
    param(
        [string]$Source,
        [string]$Message
    )

    $ast = Get-TestInputAst -Source $Source
    Assert-AstHasNoForbiddenReferences -Ast $ast
    Assert-True $true $Message
}

function Assert-CoreSourceHasNoForbiddenAstReferences {
    param([string]$Path)

    $ast = Get-TestFileAst -Path $Path
    Assert-AstHasNoForbiddenReferences -Ast $ast
    Assert-True $true 'core source has no forbidden AST references'
}

function Assert-StringSequence {
    param(
        [AllowNull()]
        [object]$Expected,
        [AllowNull()]
        [object]$Actual,
        [string]$Message
    )

    $expectedValues = [object[]]@($Expected)
    $actualValues = [object[]]@($Actual)
    if ($null -eq $Expected) {
        $expectedValues = [object[]]::new(0)
    }
    if ($null -eq $Actual) {
        $actualValues = [object[]]::new(0)
    }

    Assert-Equal $expectedValues.Count $actualValues.Count "$Message count"
    for ($index = 0; $index -lt $expectedValues.Count; $index++) {
        Assert-Equal $expectedValues[$index] $actualValues[$index] "$Message item $index"
    }
}

function ConvertTo-TestUtf8Bytes {
    param([string]$Value)

    $encoding = [System.Text.UTF8Encoding]::new($false, $true)
    return ,([byte[]]$encoding.GetBytes($Value))
}

function New-TestNulFramedBytes {
    param([string[]]$Paths)

    $encoding = [System.Text.UTF8Encoding]::new($false, $true)
    $bytes = [System.Collections.Generic.List[byte]]::new()
    foreach ($path in $Paths) {
        foreach ($byte in $encoding.GetBytes($path)) {
            [void]$bytes.Add($byte)
        }
        [void]$bytes.Add([byte]0)
    }

    return ,([byte[]]$bytes.ToArray())
}

$corePath = Join-Path $PSScriptRoot 'shell-picker-core.ps1'
. $corePath

function Test-OperationDetection {
    Assert-Equal 'cd' (Get-ShellPickerOperation -Buffer 'cd' -Cursor 2) 'detects lowercase cd at cursor 2'
    Assert-Equal 'cp' (Get-ShellPickerOperation -Buffer 'cp' -Cursor 2) 'detects lowercase cp at cursor 2'
    Assert-Null (Get-ShellPickerOperation -Buffer 'CD' -Cursor 2) 'rejects uppercase CD'
    Assert-Null (Get-ShellPickerOperation -Buffer 'Cp' -Cursor 2) 'rejects mixed-case cp'
    Assert-Null (Get-ShellPickerOperation -Buffer 'cd' -Cursor 1) 'rejects cd before cursor 2'
    Assert-Null (Get-ShellPickerOperation -Buffer 'cd' -Cursor 3) 'rejects cd after cursor 2'
    Assert-Null (Get-ShellPickerOperation -Buffer 'cd ' -Cursor 2) 'requires an exact buffer'
    Assert-Null (Get-ShellPickerOperation -Buffer 'c' -Cursor 2) 'rejects a short buffer'
    Assert-Null (Get-ShellPickerOperation -Buffer 'mv' -Cursor 2) 'rejects unknown operation'
}

function Test-NulByteConversion {
    $nullResult = ConvertFrom-ShellPickerNulBytes -Bytes $null
    Assert-ExactType -Value $nullResult.Valid -ExpectedType ([System.Boolean]) -Message 'null-byte abort Valid is exactly System.Boolean'
    Assert-True ($nullResult.Valid -is [bool]) 'null bytes return a boolean Valid property'
    Assert-True $nullResult.Valid 'null bytes are a valid abort'
    Assert-True ($nullResult.Paths -is [string[]]) 'null bytes return string[] Paths'
    Assert-ExactType -Value $nullResult.Paths -ExpectedType ([System.String[]]) -Message 'null-byte abort Paths is exactly System.String[]'
    Assert-StringSequence -Expected $null -Actual $nullResult.Paths -Message 'null bytes return no paths'

    $emptyResult = ConvertFrom-ShellPickerNulBytes -Bytes ([byte[]]@())
    Assert-ExactType -Value $emptyResult.Valid -ExpectedType ([System.Boolean]) -Message 'empty-byte abort Valid is exactly System.Boolean'
    Assert-ExactType -Value $emptyResult.Paths -ExpectedType ([System.String[]]) -Message 'empty-byte abort Paths is exactly System.String[]'
    Assert-True $emptyResult.Valid 'empty bytes are a valid abort'
    Assert-StringSequence -Expected $null -Actual $emptyResult.Paths -Message 'empty bytes return no paths'

    $unicodeOne = [string]::Concat('C:\caf', [char]0x00E9)
    $unicodeTwo = [string]::Concat('D:\', [char]0x8DEF, [char]0x5F84)
    $validBytes = New-TestNulFramedBytes -Paths @($unicodeOne, 'C:\same', $unicodeTwo, 'C:\same')
    $validResult = ConvertFrom-ShellPickerNulBytes -Bytes $validBytes
    Assert-ExactType -Value $validResult.Valid -ExpectedType ([System.Boolean]) -Message 'valid result Valid is exactly System.Boolean'
    Assert-ExactType -Value $validResult.Paths -ExpectedType ([System.String[]]) -Message 'valid result Paths is exactly System.String[]'
    Assert-True $validResult.Valid 'valid Unicode NUL bytes are accepted'
    Assert-True ($validResult.Paths -is [string[]]) 'valid NUL bytes return string[] Paths'
    Assert-StringSequence -Expected @($unicodeOne, 'C:\same', $unicodeTwo, 'C:\same') -Actual $validResult.Paths -Message 'valid paths preserve Unicode, order, and duplicates'

    $missingFinalNul = ConvertTo-TestUtf8Bytes -Value 'C:\missing-final-nul'
    $missingResult = ConvertFrom-ShellPickerNulBytes -Bytes $missingFinalNul
    Assert-ExactType -Value $missingResult.Valid -ExpectedType ([System.Boolean]) -Message 'missing-final-NUL result Valid is exactly System.Boolean'
    Assert-ExactType -Value $missingResult.Paths -ExpectedType ([System.String[]]) -Message 'missing-final-NUL result Paths is exactly System.String[]'
    Assert-False $missingResult.Valid 'missing final NUL is invalid'
    Assert-StringSequence -Expected $null -Actual $missingResult.Paths -Message 'missing final NUL rejects all paths'

    $emptyRecordResult = ConvertFrom-ShellPickerNulBytes -Bytes ([byte[]](0x00))
    Assert-ExactType -Value $emptyRecordResult.Valid -ExpectedType ([System.Boolean]) -Message 'empty-record result Valid is exactly System.Boolean'
    Assert-ExactType -Value $emptyRecordResult.Paths -ExpectedType ([System.String[]]) -Message 'empty-record result Paths is exactly System.String[]'
    Assert-False $emptyRecordResult.Valid 'empty record is invalid'
    Assert-StringSequence -Expected $null -Actual $emptyRecordResult.Paths -Message 'empty record returns no paths'

    $invalidUtf8 = [byte[]](0x43, 0x3A, 0x5C, 0xFF, 0x00)
    $invalidUtf8Result = ConvertFrom-ShellPickerNulBytes -Bytes $invalidUtf8
    Assert-ExactType -Value $invalidUtf8Result.Valid -ExpectedType ([System.Boolean]) -Message 'invalid-UTF8 result Valid is exactly System.Boolean'
    Assert-ExactType -Value $invalidUtf8Result.Paths -ExpectedType ([System.String[]]) -Message 'invalid-UTF8 result Paths is exactly System.String[]'
    Assert-False $invalidUtf8Result.Valid 'invalid UTF-8 is rejected'
    Assert-StringSequence -Expected $null -Actual $invalidUtf8Result.Paths -Message 'invalid UTF-8 returns no paths'

    $atomicBytes = [System.Collections.Generic.List[byte]]::new()
    foreach ($byte in (ConvertTo-TestUtf8Bytes -Value 'C:\accepted-before-error')) {
        [void]$atomicBytes.Add($byte)
    }
    [void]$atomicBytes.Add([byte]0)
    [void]$atomicBytes.Add([byte]0xFF)
    [void]$atomicBytes.Add([byte]0)
    $atomicResult = ConvertFrom-ShellPickerNulBytes -Bytes ([byte[]]$atomicBytes.ToArray())
    Assert-ExactType -Value $atomicResult.Valid -ExpectedType ([System.Boolean]) -Message 'atomic-rejection result Valid is exactly System.Boolean'
    Assert-ExactType -Value $atomicResult.Paths -ExpectedType ([System.String[]]) -Message 'atomic-rejection result Paths is exactly System.String[]'
    Assert-False $atomicResult.Valid 'a later malformed record invalidates the whole result'
    Assert-StringSequence -Expected $null -Actual $atomicResult.Paths -Message 'malformed input never returns partial paths'
}

function Test-SelectionValidation {
    Assert-True (Test-ShellPickerSelection -Operation 'cd' -Paths @('C:\target')) 'cd accepts exactly one nonempty path'
    Assert-True (Test-ShellPickerSelection -Operation 'cp' -Paths @('C:\one')) 'cp accepts one path'
    Assert-True (Test-ShellPickerSelection -Operation 'cp' -Paths @('C:\one', 'D:\two', 'C:\one')) 'cp accepts multiple paths and duplicates'

    Assert-False (Test-ShellPickerSelection -Operation 'cd' -Paths @()) 'cd rejects zero paths'
    Assert-False (Test-ShellPickerSelection -Operation 'cd' -Paths @('C:\one', 'D:\two')) 'cd rejects multiple paths'
    Assert-False (Test-ShellPickerSelection -Operation 'cd' -Paths @('')) 'cd rejects an empty path'
    Assert-False (Test-ShellPickerSelection -Operation 'cp' -Paths @()) 'cp rejects zero paths'
    Assert-False (Test-ShellPickerSelection -Operation 'cp' -Paths @('C:\one', '')) 'cp rejects an empty path'
    Assert-False (Test-ShellPickerSelection -Operation 'cp' -Paths @($null)) 'cp rejects a null path'

    $nulPath = [string]::Concat('C:\bad', [char]0, 'path')
    Assert-False (Test-ShellPickerSelection -Operation 'cd' -Paths @($nulPath)) 'cd rejects a NUL-containing path'
    Assert-False (Test-ShellPickerSelection -Operation 'cp' -Paths @('C:\one', $nulPath)) 'cp rejects any NUL-containing path'
    Assert-False (Test-ShellPickerSelection -Operation 'mv' -Paths @('C:\one')) 'selection rejects unknown operations'
    Assert-False (Test-ShellPickerSelection -Operation 'CD' -Paths @('C:\one')) 'selection is case-sensitive'
    Assert-False (Test-ShellPickerSelection -Operation $null -Paths @('C:\one')) 'selection rejects a null operation'
    Assert-False (Test-ShellPickerSelection -Operation 'cp' -Paths $null) 'selection rejects null paths'
}

function Test-SingleQuotedLiterals {
    Assert-Equal "'C:\one'" (ConvertTo-PowerShellSingleQuotedLiteral -Value 'C:\one') 'wraps a path in single quotes'
    Assert-Equal "'C:\it''s'" (ConvertTo-PowerShellSingleQuotedLiteral -Value "C:\it's") 'doubles apostrophes'
    Assert-Equal "''" (ConvertTo-PowerShellSingleQuotedLiteral -Value '') 'empty non-null literal is quoted'
    Assert-Null (ConvertTo-PowerShellSingleQuotedLiteral -Value $null) 'null literal input is invalid'

    $specialValue = @(
        '-',
        'C:\',
        [char]0x00E9,
        [char]96,
        '$',
        '[abc]*?',
        [char]10,
        'line',
        [char]39,
        'tail'
    ) -join ''
    $expectedSpecialLiteral = [string]::Concat("'", $specialValue.Replace("'", "''"), "'")
    $actualSpecialLiteral = ConvertTo-PowerShellSingleQuotedLiteral -Value $specialValue
    Assert-Equal $expectedSpecialLiteral $actualSpecialLiteral 'literal preserves special characters, newline, Unicode, and apostrophe escaping'

    $nulValue = [string]::Concat('before', [char]0, 'after')
    Assert-Null (ConvertTo-PowerShellSingleQuotedLiteral -Value $nulValue) 'NUL-containing literal input is invalid'
}

function Test-CopyCommandGeneration {
    $paths = @('C:\one', 'D:\two', 'C:\one')
    $command = New-ShellPickerCopyCommand -Paths $paths
    Assert-Equal "Copy-Item -LiteralPath 'C:\one', 'D:\two', 'C:\one'" $command 'copy command preserves order and duplicates'
    Assert-False $command.EndsWith(' ') 'copy command has no trailing space'

    $specialValue = @('-', 'C:\', [char]0x00E9, [char]96, '$', '[abc]*?', [char]10, 'tail', [char]39) -join ''
    $specialLiteral = ConvertTo-PowerShellSingleQuotedLiteral -Value $specialValue
    $specialCommand = New-ShellPickerCopyCommand -Paths @($specialValue, 'C:\one')
    Assert-Equal ([string]::Concat('Copy-Item -LiteralPath ', $specialLiteral, ", 'C:\one'")) $specialCommand 'copy command uses literal-safe special paths'

    Assert-Null (New-ShellPickerCopyCommand -Paths @()) 'copy command rejects zero paths'
    Assert-Null (New-ShellPickerCopyCommand -Paths @('')) 'copy command rejects empty paths'
    Assert-Null (New-ShellPickerCopyCommand -Paths @($null)) 'copy command rejects null paths'

    $nulPath = [string]::Concat('C:\bad', [char]0, 'path')
    Assert-Null (New-ShellPickerCopyCommand -Paths @($nulPath)) 'copy command rejects NUL-containing paths'
}

function Test-CoreAstContract {
    $forbiddenCommandSentinels = @(
        'PSReadLine',
        'Start-Process -FilePath sentinel',
        'Test-Path -LiteralPath sentinel',
        'Get-Item -LiteralPath sentinel',
        'Resolve-Path -LiteralPath sentinel',
        'New-Item -Path sentinel',
        'Set-Content -Path sentinel -Value sentinel',
        'Remove-Item -LiteralPath sentinel',
        'Set-Location -LiteralPath sentinel'
    )
    foreach ($source in $forbiddenCommandSentinels) {
        Assert-AstRejectsForbiddenSource -Source $source -Message "rejects forbidden command AST '$source'"
    }

    $forbiddenTypeSentinels = @(
        '[Diagnostics.Process]::GetCurrentProcess()',
        '[ProcessStartInfo]::new()',
        '[System.IO.File]::ReadAllText("sentinel")',
        '[System.IO.Directory]::GetFiles("sentinel")'
    )
    foreach ($source in $forbiddenTypeSentinels) {
        Assert-AstRejectsForbiddenSource -Source $source -Message "rejects forbidden type AST '$source'"
    }

    $forbiddenMemberSentinels = @(
        '[Environment]::GetEnvironmentVariable("sentinel")',
        '[Environment]::SetEnvironmentVariable("sentinel", "sentinel")'
    )
    foreach ($source in $forbiddenMemberSentinels) {
        Assert-AstRejectsForbiddenSource -Source $source -Message "rejects forbidden member AST '$source'"
    }

    Assert-AstRejectsForbiddenSource -Source 'Write-Output $env:PATH' -Message 'rejects Env: variable AST'

    $harmlessSource = @'
# Start-Process Test-Path [Diagnostics.Process] [System.IO.File] SetEnvironmentVariable Env:
$StartProcess = 'Start-Process'
$TestPath = 'Test-Path'
$SetContent = 'Set-Content'
$RemoveItem = 'Remove-Item'
$SetLocation = 'Set-Location'
$ProcessStartInfo = 'ProcessStartInfo'
$SetEnvironmentVariable = 'SetEnvironmentVariable'
Write-Output 'PSReadLine'
Write-Output 'Get-Item Resolve-Path New-Item System.IO.Directory GetEnvironmentVariable'
'@
    Assert-AstAllowsSource -Source $harmlessSource -Message 'ignores comments and harmless identifiers'
    Assert-CoreSourceHasNoForbiddenAstReferences -Path $corePath
}

Test-OperationDetection
Test-NulByteConversion
Test-SelectionValidation
Test-SingleQuotedLiterals
Test-CopyCommandGeneration
Test-CoreAstContract

Write-Output 'PowerShell core tests: PASS'
