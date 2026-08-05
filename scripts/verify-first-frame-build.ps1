param(
    [Parameter(Mandatory = $true)]
    [string] $ProductionOutput,

    [Parameter(Mandatory = $true)]
    [string] $HarnessOutput,

    [Parameter(Mandatory = $true)]
    [string] $MetadataOutput
)

$ErrorActionPreference = 'Stop'

$ProductionOutput = [IO.Path]::GetFullPath($ProductionOutput)
$HarnessOutput = [IO.Path]::GetFullPath($HarnessOutput)
$MetadataOutput = [IO.Path]::GetFullPath($MetadataOutput)
$Repository = Split-Path -Parent $PSScriptRoot
$productionParent = Split-Path -Parent $ProductionOutput
$harnessParent = Split-Path -Parent $HarnessOutput
$metadataParent = Split-Path -Parent $MetadataOutput
foreach ($parent in @($productionParent, $harnessParent, $metadataParent)) {
    if ($parent) {
        New-Item -ItemType Directory -Force -Path $parent | Out-Null
    }
}

$buildFlags = @('-buildvcs=false', '-trimpath', '-ldflags=-buildid=')
$staging = Join-Path $productionParent ('.first-frame-build-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $staging | Out-Null

function Invoke-GoBuild {
    param([string[]] $Arguments)

    & go @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $($LASTEXITCODE): $($Arguments -join ' ')"
    }
}

function Get-OutputPath {
    param([string] $Prefix, [int] $Index, [string] $Extension)

    Join-Path $staging (('{0}-{1}{2}' -f $Prefix, $Index, $Extension))
}

function Assert-Present {
    param([string[]] $Paths)

    $deadline = (Get-Date).AddSeconds(5)
    do {
        $missing = @($Paths | Where-Object { -not (Test-Path -LiteralPath $_ -PathType Leaf) })
        if ($missing.Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "verified build outputs disappeared or were never created: $($missing -join ', ')"
}

function Get-Sha256 {
    param([string] $Path)

    (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-SourceIdentity {
    Push-Location $Repository
    try {
        $head = (& git rev-parse HEAD 2>&1).Trim()
        if ($LASTEXITCODE -ne 0 -or $head.Length -ne 40) {
            throw "could not resolve source HEAD: $head"
        }
        [string[]]$paths = @(& git ls-files --cached --others --exclude-standard -- cmd internal integration go.mod go.sum Makefile scripts/verify-first-frame-build.ps1 |
            ForEach-Object { $_.Trim() } | Where-Object { $_ -ne '' } | Select-Object -Unique)
        [Array]::Sort($paths, [System.StringComparer]::Ordinal)
        $manifest = New-Object System.Text.StringBuilder
        [void]$manifest.Append($head).Append("`n")
        foreach ($path in $paths) {
            $normalized = $path.Replace('\', '/')
            $digest = Get-Sha256 (Join-Path $Repository $path)
            [void]$manifest.Append($normalized).Append("`t").Append($digest).Append("`n")
        }
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        $hash = [System.Security.Cryptography.SHA256]::Create()
        try {
            $sourceHash = [System.BitConverter]::ToString($hash.ComputeHash($utf8.GetBytes($manifest.ToString()))).Replace('-', '').ToLowerInvariant()
        }
        finally {
            $hash.Dispose()
        }
        return [ordered]@{ head = $head; fingerprint = "sha256:$sourceHash" }
    }
    finally {
        Pop-Location
    }
}

function Remove-FirstFrameOutputs {
    foreach ($path in @($ProductionOutput, $HarnessOutput, $MetadataOutput)) {
        if (Test-Path -LiteralPath $path) {
            Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
        }
    }
}

try {
    $productionExtension = [IO.Path]::GetExtension($ProductionOutput)
    $harnessExtension = [IO.Path]::GetExtension($HarnessOutput)
    $productionCopies = @()
    $harnessCopies = @()
    $sourceBefore = Get-SourceIdentity
    Push-Location $Repository
    try {
        foreach ($index in 1..3) {
            $output = Get-OutputPath 'production' $index $productionExtension
            $productionCopies += $output
            Invoke-GoBuild (@('build') + $buildFlags + @('-o', $output, './cmd/shell-picker'))
        }
        foreach ($index in 1..3) {
            $output = Get-OutputPath 'harness' $index $harnessExtension
            $harnessCopies += $output
            Invoke-GoBuild (@('test', '-c') + $buildFlags + @('-o', $output, './integration'))
        }
    }
    finally {
        Pop-Location
    }

    $sourceAfter = Get-SourceIdentity
    if ($sourceBefore.head -ne $sourceAfter.head -or $sourceBefore.fingerprint -ne $sourceAfter.fingerprint) {
        Remove-FirstFrameOutputs
        throw "source changed during reproducible build: before $($sourceBefore.head)/$($sourceBefore.fingerprint), after $($sourceAfter.head)/$($sourceAfter.fingerprint)"
    }

    Assert-Present ($productionCopies + $harnessCopies)
    $productionHashes = @($productionCopies | ForEach-Object { Get-Sha256 $_ })
    $harnessHashes = @($harnessCopies | ForEach-Object { Get-Sha256 $_ })
    $uniqueProductionHashes = @($productionHashes | Select-Object -Unique)
    $uniqueHarnessHashes = @($harnessHashes | Select-Object -Unique)
    if ($uniqueProductionHashes.Count -ne 1) {
        throw "production build hashes differ: $($productionHashes -join ', ')"
    }
    if ($uniqueHarnessHashes.Count -ne 1) {
        throw "harness build hashes differ: $($harnessHashes -join ', ')"
    }

    Copy-Item -LiteralPath $productionCopies[0] -Destination $ProductionOutput -Force
    Copy-Item -LiteralPath $harnessCopies[0] -Destination $HarnessOutput -Force
    Assert-Present @($ProductionOutput, $HarnessOutput)
    $productionHash = Get-Sha256 $ProductionOutput
    $harnessHash = Get-Sha256 $HarnessOutput
    if ($productionHash -ne $uniqueProductionHashes[0] -or $harnessHash -ne $uniqueHarnessHashes[0]) {
        throw 'selected reproducible build output changed while being copied'
    }

    $defenderState = 'unavailable'
    try {
        $defenderState = [string](Get-Service -Name WinDefend -ErrorAction Stop).Status
    }
    catch {
        $defenderState = 'unavailable'
    }
    $metadata = [ordered]@{
        schema = 1
        build_flags = $buildFlags
        production_sha256 = "sha256:$productionHash"
        harness_sha256 = "sha256:$harnessHash"
        source_head = $sourceBefore.head
        source_fingerprint = $sourceBefore.fingerprint
        stable_builds = 3
        files_present = $true
        defender_state = $defenderState.ToLowerInvariant()
    }
    $metadata | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $MetadataOutput -Encoding utf8NoBOM
}
finally {
    if (Test-Path -LiteralPath $staging) {
        Remove-Item -LiteralPath $staging -Recurse -Force -ErrorAction SilentlyContinue
    }
}
