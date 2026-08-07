Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:AssertionCount = 0
$script:ModuleInfo = $null

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

function Assert-Equal {
    param(
        [AllowNull()]
        [object]$Expected,
        [AllowNull()]
        [object]$Actual,
        [string]$Message
    )

    $same = if (($Expected -is [string]) -or ($Actual -is [string])) {
        ([string]::Equals([string]$Expected, [string]$Actual, [System.StringComparison]::Ordinal))
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

function Assert-Sequence {
    param(
        [AllowNull()]
        [object]$Expected,
        [AllowNull()]
        [object]$Actual,
        [string]$Message
    )

    $expectedValues = @($Expected)
    $actualValues = @($Actual)
    if ($null -eq $Expected) {
        $expectedValues = @()
    }
    if ($null -eq $Actual) {
        $actualValues = @()
    }
    Assert-Equal $expectedValues.Count $actualValues.Count "$Message count"

    for ($index = 0; $index -lt $expectedValues.Count; $index++) {
        Assert-Equal $expectedValues[$index] $actualValues[$index] "$Message item $index"
    }
}

function Assert-Throws {
    param(
        [scriptblock]$Script,
        [string]$Message
    )

    $threw = $false
    try {
        & $Script
    }
    catch {
        $threw = $true
    }

    Assert-True $threw $Message
}

function Invoke-ModulePrivate {
    param(
        [scriptblock]$Script,
        [AllowNull()]
        [object[]]$Arguments
    )

    & $script:ModuleInfo $Script @Arguments
}

function Register-TestPicker {
    param([string]$Path)

    Invoke-ModulePrivate {
        param([string]$PickerPath)
        Register-ShellPicker -PickerPath $PickerPath
    } @($Path)
}

function Get-KeyHandlerSnapshot {
    return @(
        Get-PSReadLineKeyHandler |
            Where-Object { $_.Key -ne 'Spacebar' } |
            ForEach-Object {
                [string]::Concat(
                    [string]$_.Key,
                    [char]0,
                    [string]$_.Function,
                    [char]0,
                    [string]$_.Description,
                    [char]0,
                    [string]$_.Group
                )
            } |
            Sort-Object
    )
}

function Get-FileAst {
    param([string]$Path)

    $tokens = $null
    $parseErrors = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseFile(
        $Path,
        [ref]$tokens,
        [ref]$parseErrors
    )
    if (($null -ne $parseErrors) -and ($parseErrors.Count -ne 0)) {
        throw [System.Exception]::new("adapter source failed to parse: $($parseErrors[0].Message)")
    }

    return $ast
}

function New-TestFakeProcess {
    param(
        [byte[]]$OutputBytes,
        [int]$ExitCode = 0,
        [bool]$ThrowOnCopy = $false
    )

    $stream = if ($ThrowOnCopy) {
        $throwingStream = [pscustomobject]@{}
        Add-Member -InputObject $throwingStream -MemberType ScriptMethod -Name CopyTo -Value {
            param([object]$Destination)
            throw [System.IO.IOException]::new('test output drain failure')
        }
        $throwingStream
    }
    else {
        [System.IO.MemoryStream]::new($OutputBytes)
    }

    $process = [pscustomobject]@{
        ExitCode = $ExitCode
        HasExited = $false
        KillCalls = 0
        KillTree = $false
        WaitForExitCalls = 0
        LastWaitTimeout = -1
        DisposeCalls = 0
        Started = $false
        StandardOutput = [pscustomobject]@{
            BaseStream = $stream
        }
    }

    Add-Member -InputObject $process -MemberType ScriptMethod -Name Start -Value {
        $this.Started = $true
        return $true
    }
    Add-Member -InputObject $process -MemberType ScriptMethod -Name WaitForExit -Value {
        param([int]$Timeout = -1)
        $this.WaitForExitCalls++
        $this.LastWaitTimeout = $Timeout
        $this.HasExited = $true
    }
    Add-Member -InputObject $process -MemberType ScriptMethod -Name Kill -Value {
        param([bool]$EntireProcessTree)
        $this.KillCalls++
        $this.KillTree = $EntireProcessTree
        $this.HasExited = $true
    }
    Add-Member -InputObject $process -MemberType ScriptMethod -Name Dispose -Value {
        $this.DisposeCalls++
    }

    return $process
}

function Set-TestProcessFactory {
    param([object]$Process)

    Invoke-ModulePrivate {
        param([object]$InjectedProcess)
        $script:InjectedProcess = $InjectedProcess
        $script:FactoryCalls = 0
        $script:CapturedStartInfo = $null
        $script:ProcessFactory = {
            param([System.Diagnostics.ProcessStartInfo]$StartInfo)
            $script:FactoryCalls++
            $script:CapturedStartInfo = $StartInfo
            return $script:InjectedProcess
        }
    } @($Process)
}

function Test-ManifestContract {
    $manifestPath = Join-Path $PSScriptRoot 'shell-picker.psd1'
    Assert-True (Test-Path -LiteralPath $manifestPath -PathType Leaf) 'adapter manifest exists'

    $manifest = Import-PowerShellDataFile -LiteralPath $manifestPath
    Assert-Equal 'shell-picker.psm1' $manifest.RootModule 'manifest points to the module root'
    Assert-True ([version]::Parse([string]$manifest.ModuleVersion) -ge [version]'1.0.0') 'manifest has a released module version'
    Assert-Equal '7.4.7' ([string]$manifest.PowerShellVersion) 'manifest requires PowerShell 7.4.7'

    $requiredModules = @($manifest.RequiredModules)
    $psReadLineRequirement = @(
        $requiredModules |
            Where-Object {
                ($_ -is [hashtable]) -and ([string]$_.ModuleName -ceq 'PSReadLine')
            }
    )
    Assert-Equal 1 $psReadLineRequirement.Count 'manifest has one PSReadLine requirement'
    Assert-True (
        [version]::Parse([string]$psReadLineRequirement[0].ModuleVersion) -ge [version]'2.3.6'
    ) 'manifest requires PSReadLine 2.3.6 or newer'

    Assert-Sequence @('Register-ShellPicker') $manifest.FunctionsToExport 'manifest exports only the public function'
    Assert-Sequence @() $manifest.CmdletsToExport 'manifest exports no cmdlets'
    Assert-Sequence @() $manifest.VariablesToExport 'manifest exports no variables'
    Assert-Sequence @() $manifest.AliasesToExport 'manifest exports no aliases'
}

function Test-ModuleExports {
    $modulePath = Join-Path $PSScriptRoot 'shell-picker.psd1'
    $script:ModuleInfo = Import-Module -Name $modulePath -Force -PassThru
    $exportedCommands = @(
        Get-Command -Module $script:ModuleInfo.Name |
            Where-Object { $_.CommandType -in @('Function', 'Cmdlet', 'Alias') } |
            Select-Object -ExpandProperty Name |
            Sort-Object
    )
    Assert-Sequence @('Register-ShellPicker') $exportedCommands 'module exports only Register-ShellPicker'
}

function Test-RegistrationAndResolution {
    $pickerPath = Join-Path $PSScriptRoot 'shell-picker-core.ps1'
    $beforeHandlers = Get-KeyHandlerSnapshot

    Assert-Throws {
        Register-TestPicker -Path (Join-Path $PSScriptRoot 'does-not-exist.exe')
    } 'registration rejects a missing picker path'
    Assert-Throws {
        Register-TestPicker -Path $PSScriptRoot
    } 'registration rejects a directory picker path'

    Register-TestPicker -Path $pickerPath
    $afterFirstRegistration = Get-KeyHandlerSnapshot
    $firstHandler = Invoke-ModulePrivate { $script:SpaceHandler }

    Register-TestPicker -Path $pickerPath
    $afterSecondRegistration = Get-KeyHandlerSnapshot
    $secondHandler = Invoke-ModulePrivate { $script:SpaceHandler }

    Assert-Sequence $beforeHandlers $afterFirstRegistration 'registration leaves every non-Space binding unchanged'
    Assert-Sequence $afterFirstRegistration $afterSecondRegistration 'repeated registration leaves every non-Space binding unchanged'
    Assert-True ([object]::ReferenceEquals($firstHandler, $secondHandler)) 'registration reuses one stable Spacebar scriptblock'

    $spaceHandlers = @(Get-PSReadLineKeyHandler -Chord Spacebar)
    Assert-Equal 1 $spaceHandlers.Count 'registration owns one Spacebar binding'
    Assert-Equal 'CustomAction' $spaceHandlers[0].Function 'Spacebar is owned by the custom handler'

    $temporaryPath = [System.IO.Path]::GetTempFileName()
    try {
        $currentDirectory = (Get-Location).ProviderPath
        $relativeTemporaryPath = [System.IO.Path]::GetRelativePath($currentDirectory, $temporaryPath)
        Register-TestPicker -Path $relativeTemporaryPath
        Remove-Item -LiteralPath $temporaryPath -Force -ErrorAction Stop

        $startInfo = Invoke-ModulePrivate {
            New-ShellPickerProcessStartInfo -Operation 'cd' -Cwd 'C:\work dir' -Home 'C:\home dir'
        }
        Assert-Equal ([System.IO.Path]::GetFullPath($temporaryPath)) $startInfo.FileName 'registration resolves and stores the absolute picker path once'
    }
    finally {
        if (Test-Path -LiteralPath $temporaryPath -PathType Leaf) {
            Remove-Item -LiteralPath $temporaryPath -Force
        }
        Register-TestPicker -Path $pickerPath
    }
}

function Test-ProcessStartInfoContract {
    $pickerPath = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'shell-picker-core.ps1'))
    Register-TestPicker -Path $pickerPath

    $startInfo = Invoke-ModulePrivate {
        New-ShellPickerProcessStartInfo -Operation 'cp' -Cwd 'C:\work dir' -Home 'C:\home dir'
    }

    Assert-ExactType $startInfo ([System.Diagnostics.ProcessStartInfo]) 'private start-info helper returns ProcessStartInfo'
    Assert-False $startInfo.UseShellExecute 'picker process does not use shell execution'
    Assert-Equal $pickerPath $startInfo.FileName 'picker process uses the stored absolute file path'
    Assert-True $startInfo.RedirectStandardOutput 'picker process redirects stdout'
    Assert-False $startInfo.RedirectStandardInput 'picker process inherits stdin'
    Assert-False $startInfo.RedirectStandardError 'picker process inherits stderr'
    Assert-False $startInfo.CreateNoWindow 'picker process does not suppress its window'
    Assert-Equal '' $startInfo.WorkingDirectory 'picker process does not replace the inherited working directory'
    Assert-Sequence @('cp', '--cwd', 'C:\work dir', '--home', 'C:\home dir', '--output', 'nul') $startInfo.ArgumentList 'picker process receives the exact argument sequence'
}

function Assert-ProcessResult {
    param(
        [object]$Result,
        [bool]$ExpectedSuccess,
        [byte[]]$ExpectedBytes,
        [string]$Message
    )

    Assert-ExactType $Result ([System.Management.Automation.PSCustomObject]) "$Message result is a custom object"
    Assert-ExactType $Result.Success ([System.Boolean]) "$Message Success is exactly Boolean"
    Assert-ExactType $Result.Bytes ([System.Byte[]]) "$Message Bytes is exactly byte[]"
    Assert-Equal $ExpectedSuccess $Result.Success "$Message Success"
    Assert-Sequence $ExpectedBytes $Result.Bytes "$Message Bytes"
}

function Test-ProcessResultAndCleanup {
    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()

    $successProcess = New-TestFakeProcess -OutputBytes ([byte[]](0x41, 0x00, 0x42))
    Set-TestProcessFactory -Process $successProcess
    $success = Invoke-ModulePrivate {
        Invoke-ShellPickerProcess -StartInfo $args[0]
    } @($startInfo)
    Assert-ProcessResult $success $true ([byte[]](0x41, 0x00, 0x42)) 'successful process'
    Assert-True $successProcess.Started 'successful process is started'
    Assert-Equal 1 $successProcess.WaitForExitCalls 'successful process waits after draining stdout'
    Assert-Equal 0 $successProcess.KillCalls 'successful process is not killed'
    Assert-Equal 1 $successProcess.DisposeCalls 'successful process is disposed'

    $emptyProcess = New-TestFakeProcess -OutputBytes ([byte[]]::new(0))
    Set-TestProcessFactory -Process $emptyProcess
    $empty = Invoke-ModulePrivate {
        Invoke-ShellPickerProcess -StartInfo $args[0]
    } @($startInfo)
    Assert-ProcessResult $empty $true ([byte[]]::new(0)) 'successful empty process'
    Assert-Equal 1 $emptyProcess.DisposeCalls 'successful empty process is disposed'

    $nonzeroProcess = New-TestFakeProcess -OutputBytes ([byte[]](0x41)) -ExitCode 7
    Set-TestProcessFactory -Process $nonzeroProcess
    $nonzero = Invoke-ModulePrivate {
        Invoke-ShellPickerProcess -StartInfo $args[0]
    } @($startInfo)
    Assert-ProcessResult $nonzero $false ([byte[]]::new(0)) 'nonzero process'
    Assert-Equal 0 $nonzeroProcess.KillCalls 'exited nonzero process is not killed'
    Assert-Equal 1 $nonzeroProcess.DisposeCalls 'nonzero process is disposed'

    $failedDrainProcess = New-TestFakeProcess -OutputBytes ([byte[]]::new(0)) -ThrowOnCopy $true
    Set-TestProcessFactory -Process $failedDrainProcess
    $failedDrain = Invoke-ModulePrivate {
        Invoke-ShellPickerProcess -StartInfo $args[0]
    } @($startInfo)
    Assert-ProcessResult $failedDrain $false ([byte[]]::new(0)) 'failed stdout drain'
    Assert-Equal 1 $failedDrainProcess.KillCalls 'live failed process is killed'
    Assert-True $failedDrainProcess.KillTree 'live failed process kills its process tree'
    Assert-Equal 1 $failedDrainProcess.WaitForExitCalls 'failed process uses bounded WaitForExit cleanup'
    Assert-True (($failedDrainProcess.LastWaitTimeout -ge 0) -and ($failedDrainProcess.LastWaitTimeout -le 5000)) 'failed process cleanup wait is bounded'
    Assert-Equal 1 $failedDrainProcess.DisposeCalls 'failed process is disposed'
}

function Test-SourceSafetyGuards {
    $modulePath = Join-Path $PSScriptRoot 'shell-picker.psm1'
    $source = [System.IO.File]::ReadAllText($modulePath)
    $ast = Get-FileAst -Path $modulePath

    $forbiddenCommandNames = @(
        'Start-Process',
        'Get-Process',
        'Stop-Process',
        'Get-WmiObject',
        'Get-CimInstance',
        'Start-Job',
        'Start-ThreadJob',
        'Register-EngineEvent',
        'Register-ObjectEvent',
        'Invoke-Expression',
        'Invoke-Command',
        'Out-File',
        'Set-Content',
        'Add-Content',
        'New-TemporaryFile'
    )
    $commandAsts = @($ast.FindAll({
        param($node)
        $node -is [System.Management.Automation.Language.CommandAst]
    }, $true))
    foreach ($commandAst in $commandAsts) {
        $commandName = $commandAst.GetCommandName()
        Assert-False ($forbiddenCommandNames -contains $commandName) "module source has no forbidden command '$commandName'"
    }

    $forbiddenPatterns = @(
        '(?i)\btaskkill(?:\.exe)?\b',
        '(?i)\bpwsh(?:\.exe)?\b',
        '(?i)(?<![\w.])powershell(?:\.exe)?\b',
        '(?i)\bWMI\b',
        '\$env:',
        '\[Environment',
        'EnvironmentVariable',
        '\.Environment\b',
        '(?i)temporary[ ._-]*output',
        '(?i)sidecar'
    )
    foreach ($pattern in $forbiddenPatterns) {
        Assert-False ($source -match $pattern) "module source has no forbidden mechanism matching '$pattern'"
    }

    $setKeyCommands = @($commandAsts | Where-Object { $_.GetCommandName() -eq 'Set-PSReadLineKeyHandler' })
    Assert-Equal 1 $setKeyCommands.Count 'module registers exactly one PSReadLine key handler'
    Assert-True ($source -match "Set-PSReadLineKeyHandler\s+-Chord\s+'Spacebar'") 'module owns Spacebar only'
    Assert-False ($source -match '(?i)Set-PSReadLineOption|Register-(?:Engine|Object)Event|OnIdle|IdleHandler') 'module has no prompt, idle, or option hooks'

    $environmentVariables = @($ast.FindAll({
        param($node)
        if ($node -isnot [System.Management.Automation.Language.VariableExpressionAst]) {
            return $false
        }

        $node.VariablePath.IsDriveQualified -and ($node.VariablePath.DriveName -ieq 'env')
    }, $true))
    Assert-Equal 0 $environmentVariables.Count 'module does not mutate or inject environment variables'

    Assert-True ($source -match '\[Microsoft\.PowerShell\.PSConsoleReadLine\]::GetBufferState') 'Spacebar handler reads the buffer state'
    Assert-True ($source -match '\[Microsoft\.PowerShell\.PSConsoleReadLine\]::Insert\(') 'Spacebar handler always inserts through PSReadLine'
    Assert-True ($source -match '\[Microsoft\.PowerShell\.PSConsoleReadLine\]::Replace\(') 'Spacebar handler replaces through PSReadLine'
    Assert-True ($source -match '\[Microsoft\.PowerShell\.PSConsoleReadLine\]::InvokePrompt\(') 'directory changes refresh the prompt through PSReadLine'
}

Test-ManifestContract
Test-ModuleExports
Test-RegistrationAndResolution
Test-ProcessStartInfoContract
Test-ProcessResultAndCleanup
Test-SourceSafetyGuards

Write-Output "PowerShell adapter tests: PASS ($script:AssertionCount assertions)"
