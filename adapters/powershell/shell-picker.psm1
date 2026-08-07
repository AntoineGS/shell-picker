Set-StrictMode -Version Latest

$corePath = Join-Path $PSScriptRoot 'shell-picker-core.ps1'
. $corePath

$script:PickerPath = $null

$script:ProcessFactory = {
    param([System.Diagnostics.ProcessStartInfo]$StartInfo)

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $StartInfo
    return $process
}

function New-ShellPickerProcessResult {
    param(
        [bool]$Success,
        [AllowNull()]
        [byte[]]$Bytes
    )

    if ($null -eq $Bytes) {
        $resultBytes = [byte[]]::new(0)
    }
    else {
        $resultBytes = [byte[]]$Bytes
    }
    return [pscustomobject]@{
        Success = [bool]$Success
        Bytes = $resultBytes
    }
}

function New-ShellPickerProcessStartInfo {
    param(
        [ValidateSet('cd', 'cp')]
        [string]$Operation,
        [string]$Cwd,
        [string]$Home
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.UseShellExecute = $false
    $startInfo.FileName = $script:PickerPath
    [void]$startInfo.ArgumentList.Add($Operation)
    [void]$startInfo.ArgumentList.Add('--cwd')
    [void]$startInfo.ArgumentList.Add($Cwd)
    [void]$startInfo.ArgumentList.Add('--home')
    [void]$startInfo.ArgumentList.Add($Home)
    [void]$startInfo.ArgumentList.Add('--output')
    [void]$startInfo.ArgumentList.Add('nul')
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardInput = $false
    $startInfo.RedirectStandardError = $false
    $startInfo.CreateNoWindow = $false
    return $startInfo
}

function Invoke-ShellPickerProcess {
    param(
        [System.Diagnostics.ProcessStartInfo]$StartInfo
    )

    $process = $null
    $started = $false
    $memoryStream = $null

    try {
        $process = & $script:ProcessFactory $StartInfo
        if ($null -eq $process) {
            throw [System.InvalidOperationException]::new('picker process factory returned no process')
        }

        $started = [bool]$process.Start()
        if (-not $started) {
            throw [System.InvalidOperationException]::new('picker process did not start')
        }

        $memoryStream = [System.IO.MemoryStream]::new()
        $process.StandardOutput.BaseStream.CopyTo($memoryStream)
        $process.WaitForExit()

        if ([int]$process.ExitCode -ne 0) {
            return New-ShellPickerProcessResult -Success $false -Bytes ([byte[]]::new(0))
        }

        return New-ShellPickerProcessResult -Success $true -Bytes ([byte[]]$memoryStream.ToArray())
    }
    catch {
        if ($started -and ($null -ne $process)) {
            $isLive = $true
            try {
                $isLive = -not [bool]$process.HasExited
            }
            catch {
                $isLive = $true
            }

            if ($isLive) {
                try {
                    $process.Kill($true)
                }
                catch {
                }
                try {
                    [void]$process.WaitForExit(1000)
                }
                catch {
                }
            }
        }

        return New-ShellPickerProcessResult -Success $false -Bytes ([byte[]]::new(0))
    }
    finally {
        if ($null -ne $memoryStream) {
            $memoryStream.Dispose()
        }
        if ($null -ne $process) {
            $process.Dispose()
        }
    }
}

function Invoke-ShellPickerOperation {
    param([string]$Operation)

    try {
        $location = Get-Location -ErrorAction Stop
        if ($location.Provider.Name -cne 'FileSystem') {
            return
        }

        $startInfo = New-ShellPickerProcessStartInfo -Operation $operation -Cwd $location.ProviderPath -Home $HOME
        $result = Invoke-ShellPickerProcess -StartInfo $startInfo
        if (-not $result.Success) {
            return
        }

        $parsed = ConvertFrom-ShellPickerNulBytes -Bytes $result.Bytes
        if (-not $parsed.Valid) {
            return
        }
        if (-not (Test-ShellPickerSelection -Operation $operation -Paths $parsed.Paths)) {
            return
        }

        if ($operation -ceq 'cd') {
            Set-Location -LiteralPath $parsed.Paths[0] -ErrorAction Stop
            $previousOutputEncoding = [Console]::OutputEncoding
            try {
                [Console]::OutputEncoding = [Text.Encoding]::UTF8
                [Microsoft.PowerShell.PSConsoleReadLine]::Replace(0, 3, '')
                [Microsoft.PowerShell.PSConsoleReadLine]::InvokePrompt()
            }
            finally {
                [Console]::OutputEncoding = $previousOutputEncoding
            }
            return
        }

        $command = New-ShellPickerCopyCommand -Paths $parsed.Paths
        if ($null -eq $command) {
            return
        }

        [Microsoft.PowerShell.PSConsoleReadLine]::Replace(0, 3, $command)
    }
    catch {
        return
    }
}

function Test-ShellPickerSingleInsertionArgument {
    param(
        [AllowNull()]
        [object]$Argument
    )

    if ($null -eq $Argument) {
        return $true
    }

    return ($Argument -is [int]) -and ([int]$Argument -eq 1)
}

$script:SpaceRuntime = [pscustomobject]@{
    GetBufferState = {
        $buffer = ''
        $cursor = 0
        [Microsoft.PowerShell.PSConsoleReadLine]::GetBufferState([ref]$buffer, [ref]$cursor)
        return [pscustomobject]@{
            Buffer = $buffer
            Cursor = $cursor
        }
    }
    GetSelectionState = {
        $selectionStart = 0
        $selectionLength = 0
        [Microsoft.PowerShell.PSConsoleReadLine]::GetSelectionState([ref]$selectionStart, [ref]$selectionLength)
        return $selectionLength
    }
    SelfInsert = {
        param($Key, $Argument)
        [Microsoft.PowerShell.PSConsoleReadLine]::SelfInsert($Key, $Argument)
    }
    InvokePicker = {
        param([string]$Operation)
        Invoke-ShellPickerOperation -Operation $Operation
    }
}

function Invoke-ShellPickerSpace {
    param(
        [AllowNull()]
        [object]$Key,
        [AllowNull()]
        [object]$Argument
    )

    try {
        $getBufferState = $script:SpaceRuntime.GetBufferState
        $bufferState = & $getBufferState
        $getSelectionState = $script:SpaceRuntime.GetSelectionState
        $selectionLength = [int](& $getSelectionState)
        $operation = Get-ShellPickerOperation -Buffer $bufferState.Buffer -Cursor $bufferState.Cursor

        $selfInsert = $script:SpaceRuntime.SelfInsert
        & $selfInsert $Key $Argument

        if (($selectionLength -gt 0) -or ($null -eq $operation) -or
            (-not (Test-ShellPickerSingleInsertionArgument -Argument $Argument))) {
            return
        }

        $invokePicker = $script:SpaceRuntime.InvokePicker
        & $invokePicker $operation
    }
    catch {
        return
    }
}

$script:SpaceHandler = {
    param($Key, $Argument)
    Invoke-ShellPickerSpace -Key $Key -Argument $Argument
}

function Register-ShellPicker {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory, Position = 0)]
        [ValidateNotNullOrEmpty()]
        [string]$PickerPath
    )

    $resolved = Resolve-Path -LiteralPath $PickerPath -ErrorAction Stop
    if (($null -eq $resolved) -or ($resolved.Provider.Name -cne 'FileSystem')) {
        throw [System.ArgumentException]::new('PickerPath must resolve to a file-system path.', 'PickerPath')
    }
    if (-not (Test-Path -LiteralPath $resolved.ProviderPath -PathType Leaf)) {
        throw [System.ArgumentException]::new('PickerPath must resolve to a file.', 'PickerPath')
    }

    $script:PickerPath = [System.IO.Path]::GetFullPath($resolved.ProviderPath)
    $handler = @{
        Chord = 'Spacebar'
        ScriptBlock = $script:SpaceHandler
        BriefDescription = 'shell-picker'
        Description = 'Launch shell-picker after cd or cp'
    }
    if ((Get-PSReadLineOption).EditMode -eq 'Vi') {
        $handler.ViMode = 'Insert'
    }
    Set-PSReadLineKeyHandler @handler
}

Export-ModuleMember -Function Register-ShellPicker
