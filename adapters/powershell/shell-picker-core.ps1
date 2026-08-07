function Get-ShellPickerOperation {
    param(
        [AllowNull()]
        [string]$Buffer,
        [int]$Cursor
    )

    if ($Cursor -ne 2) {
        return $null
    }

    if (($Buffer -ceq 'cd') -or ($Buffer -ceq 'cp')) {
        return $Buffer
    }

    return $null
}

function ConvertFrom-ShellPickerNulBytes {
    param(
        [AllowNull()]
        [byte[]]$Bytes
    )

    if (($null -eq $Bytes) -or ($Bytes.Length -eq 0)) {
        return [pscustomobject]@{
            Valid = $true
            Paths = [string[]]@()
        }
    }

    $encoding = [System.Text.UTF8Encoding]::new($false, $true)
    $paths = [System.Collections.Generic.List[string]]::new()
    $recordStart = 0

    for ($index = 0; $index -lt $Bytes.Length; $index++) {
        if ($Bytes[$index] -ne 0) {
            continue
        }

        $recordLength = $index - $recordStart
        if ($recordLength -eq 0) {
            return [pscustomobject]@{
                Valid = $false
                Paths = [string[]]@()
            }
        }

        try {
            $paths.Add($encoding.GetString($Bytes, $recordStart, $recordLength))
        }
        catch [System.Text.DecoderFallbackException] {
            return [pscustomobject]@{
                Valid = $false
                Paths = [string[]]@()
            }
        }

        $recordStart = $index + 1
    }

    if ($recordStart -ne $Bytes.Length) {
        return [pscustomobject]@{
            Valid = $false
            Paths = [string[]]@()
        }
    }

    return [pscustomobject]@{
        Valid = $true
        Paths = [string[]]$paths.ToArray()
    }
}

function Test-ShellPickerSelection {
    param(
        [AllowNull()]
        [string]$Operation,
        [AllowNull()]
        [string[]]$Paths
    )

    if (($Operation -cne 'cd') -and ($Operation -cne 'cp')) {
        return $false
    }

    if ($null -eq $Paths) {
        return $false
    }

    if (($Operation -ceq 'cd') -and ($Paths.Count -ne 1)) {
        return $false
    }

    if (($Operation -ceq 'cp') -and ($Paths.Count -lt 1)) {
        return $false
    }

    foreach ($path in $Paths) {
        if (($null -eq $path) -or ($path.Length -eq 0) -or ($path.IndexOf([char]0) -ge 0)) {
            return $false
        }
    }

    return $true
}

function ConvertTo-PowerShellSingleQuotedLiteral {
    param(
        [AllowNull()]
        [object]$Value
    )

    if ($null -eq $Value) {
        return $null
    }

    $valueText = [string]$Value
    if ($valueText.IndexOf([char]0) -ge 0) {
        return $null
    }

    return [string]::Concat("'", $valueText.Replace("'", "''"), "'")
}

function New-ShellPickerCopyCommand {
    param(
        [AllowNull()]
        [string[]]$Paths
    )

    if (-not (Test-ShellPickerSelection -Operation 'cp' -Paths $Paths)) {
        return $null
    }

    $literals = [System.Collections.Generic.List[string]]::new()
    foreach ($path in $Paths) {
        $literal = ConvertTo-PowerShellSingleQuotedLiteral -Value $path
        if ($null -eq $literal) {
            return $null
        }

        [void]$literals.Add($literal)
    }

    return [string]::Concat('Copy-Item -LiteralPath ', ($literals -join ', '))
}
