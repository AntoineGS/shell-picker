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

$script:SpaceHandler = {
    param($Key, $Arg)

    $buffer = ''
    $cursor = 0
    [Microsoft.PowerShell.PSConsoleReadLine]::GetBufferState([ref]$buffer, [ref]$cursor)
    $operation = Get-ShellPickerOperation -Buffer $buffer -Cursor $cursor
    [Microsoft.PowerShell.PSConsoleReadLine]::Insert(' ')

    if ($null -eq $operation) {
        return
    }

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
            [Microsoft.PowerShell.PSConsoleReadLine]::Replace(0, 3, '')
            [Microsoft.PowerShell.PSConsoleReadLine]::InvokePrompt()
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
    Set-PSReadLineKeyHandler -Chord 'Spacebar' -ScriptBlock $script:SpaceHandler
}

Export-ModuleMember -Function Register-ShellPicker
