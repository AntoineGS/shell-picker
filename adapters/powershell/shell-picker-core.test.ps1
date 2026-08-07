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

function Assert-SourceHasNoForbiddenReference {
    param(
        [string]$Source,
        [string]$Reference
    )

    if ($Source.IndexOf($Reference, [System.StringComparison]::OrdinalIgnoreCase) -ge 0) {
        throw [System.Exception]::new("core source contains forbidden reference '$Reference'")
    }

    [void]($script:AssertionCount++)
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
    Assert-StringSequence -Expected $null -Actual $nullResult.Paths -Message 'null bytes return no paths'

    $emptyResult = ConvertFrom-ShellPickerNulBytes -Bytes ([byte[]]@())
    Assert-ExactType -Value $emptyResult.Valid -ExpectedType ([System.Boolean]) -Message 'empty-byte abort Valid is exactly System.Boolean'
    Assert-True $emptyResult.Valid 'empty bytes are a valid abort'
    Assert-StringSequence -Expected $null -Actual $emptyResult.Paths -Message 'empty bytes return no paths'

    $unicodeOne = [string]::Concat('C:\caf', [char]0x00E9)
    $unicodeTwo = [string]::Concat('D:\', [char]0x8DEF, [char]0x5F84)
    $validBytes = New-TestNulFramedBytes -Paths @($unicodeOne, 'C:\same', $unicodeTwo, 'C:\same')
    $validResult = ConvertFrom-ShellPickerNulBytes -Bytes $validBytes
    Assert-ExactType -Value $validResult.Valid -ExpectedType ([System.Boolean]) -Message 'valid result Valid is exactly System.Boolean'
    Assert-True $validResult.Valid 'valid Unicode NUL bytes are accepted'
    Assert-True ($validResult.Paths -is [string[]]) 'valid NUL bytes return string[] Paths'
    Assert-StringSequence -Expected @($unicodeOne, 'C:\same', $unicodeTwo, 'C:\same') -Actual $validResult.Paths -Message 'valid paths preserve Unicode, order, and duplicates'

    $missingFinalNul = ConvertTo-TestUtf8Bytes -Value 'C:\missing-final-nul'
    $missingResult = ConvertFrom-ShellPickerNulBytes -Bytes $missingFinalNul
    Assert-ExactType -Value $missingResult.Valid -ExpectedType ([System.Boolean]) -Message 'missing-final-NUL result Valid is exactly System.Boolean'
    Assert-False $missingResult.Valid 'missing final NUL is invalid'
    Assert-StringSequence -Expected $null -Actual $missingResult.Paths -Message 'missing final NUL rejects all paths'

    $emptyRecordResult = ConvertFrom-ShellPickerNulBytes -Bytes ([byte[]](0x00))
    Assert-ExactType -Value $emptyRecordResult.Valid -ExpectedType ([System.Boolean]) -Message 'empty-record result Valid is exactly System.Boolean'
    Assert-False $emptyRecordResult.Valid 'empty record is invalid'
    Assert-StringSequence -Expected $null -Actual $emptyRecordResult.Paths -Message 'empty record returns no paths'

    $invalidUtf8 = [byte[]](0x43, 0x3A, 0x5C, 0xFF, 0x00)
    $invalidUtf8Result = ConvertFrom-ShellPickerNulBytes -Bytes $invalidUtf8
    Assert-ExactType -Value $invalidUtf8Result.Valid -ExpectedType ([System.Boolean]) -Message 'invalid-UTF8 result Valid is exactly System.Boolean'
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

function Test-CoreSourceContract {
    $coreSource = [System.IO.File]::ReadAllText($corePath)
    $forbiddenReferences = @(
        'PSReadLine',
        'Diagnostics.Process',
        'ProcessStartInfo',
        'Test-Path',
        'Get-Item',
        'Set-Location',
        'Env:',
        'GetEnvironmentVariable'
    )

    foreach ($reference in $forbiddenReferences) {
        Assert-SourceHasNoForbiddenReference -Source $coreSource -Reference $reference
    }
}

Test-OperationDetection
Test-NulByteConversion
Test-SelectionValidation
Test-SingleQuotedLiterals
Test-CopyCommandGeneration
Test-CoreSourceContract

Write-Output 'PowerShell core tests: PASS'
