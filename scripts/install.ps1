param(
    [string]$Version = 'latest',
    [string]$InstallDir = ''
)

$ErrorActionPreference = 'Stop'
$repo = 'lihongjie0209/relaycat'
$client = New-Object System.Net.WebClient
$client.Headers.Add('User-Agent', 'relaycat-installer')
$tempDir = $null
$staged = $null

function Download-File([string]$Url, [string]$Path) {
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        try {
            $client.DownloadFile($Url, $Path)
            return
        } catch {
            if ([IO.File]::Exists($Path)) { [IO.File]::Delete($Path) }
            if ($attempt -eq 3) { throw }
            Start-Sleep -Seconds $attempt
        }
    }
}

function Download-Text([string]$Url) {
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        try { return $client.DownloadString($Url) } catch {
            if ($attempt -eq 3) { throw }
            Start-Sleep -Seconds $attempt
        }
    }
}

try {
    # Windows 7's default .NET settings may omit TLS 1.2.
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]([int][Net.ServicePointManager]::SecurityProtocol -bor 3072)

    if ($Version -eq 'latest') {
        $release = Download-Text "https://api.github.com/repos/$repo/releases/latest"
        $match = [regex]::Match($release, '"tag_name"\s*:\s*"([^"]+)"')
        if (-not $match.Success) { throw 'Could not find the latest release tag.' }
        $Version = $match.Groups[1].Value
    }
    if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+([-+][A-Za-z0-9.-]+)?$') {
        throw "Invalid release version: $Version"
    }

    $cpu = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrEmpty($cpu)) { $cpu = $env:PROCESSOR_ARCHITECTURE }
    switch ($cpu.ToUpperInvariant()) {
        'AMD64' { $arch = 'amd64' }
        'X86'   { $arch = '386' }
        'ARM64' { $arch = 'arm64' }
        default { throw "Unsupported Windows CPU architecture: $cpu" }
    }

    if ([string]::IsNullOrEmpty($InstallDir)) {
        $localAppData = [Environment]::GetFolderPath('LocalApplicationData')
        if ([string]::IsNullOrEmpty($localAppData)) { throw 'LocalAppData is unavailable; pass -InstallDir.' }
        $InstallDir = Join-Path $localAppData 'Programs\Relaycat'
    }

    $archive = "relaycat_$($Version.Substring(1))_windows_$arch.zip"
    $base = "https://github.com/$repo/releases/download/$Version"
    $tempDir = Join-Path ([IO.Path]::GetTempPath()) ('relaycat-install-' + [Guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($tempDir) | Out-Null
    $archivePath = Join-Path $tempDir $archive
    $checksumsPath = Join-Path $tempDir 'checksums.txt'
    Download-File "$base/checksums.txt" $checksumsPath
    Download-File "$base/$archive" $archivePath

    $pattern = '^[0-9a-fA-F]{64}\s+' + [regex]::Escape($archive) + '$'
    $lines = @([IO.File]::ReadAllLines($checksumsPath) | Where-Object { $_ -match $pattern })
    if ($lines.Count -ne 1) { throw "Checksum entry missing or ambiguous for $archive." }
    $expected = ($lines[0] -split '\s+')[0].ToLowerInvariant()
    $stream = [IO.File]::OpenRead($archivePath)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $actual = [BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()
    } finally {
        $stream.Dispose()
        $sha.Dispose()
    }
    if ($actual -ne $expected) { throw "SHA-256 mismatch for $archive." }

    $extractDir = Join-Path $tempDir 'extracted'
    [IO.Directory]::CreateDirectory($extractDir) | Out-Null
    try {
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        [IO.Compression.ZipFile]::ExtractToDirectory($archivePath, $extractDir)
    } catch {
        # The Shell COM extractor also works on Windows 7 without .NET 4.5.
        $shell = New-Object -ComObject Shell.Application
        $zipFolder = $shell.NameSpace($archivePath)
        $outputFolder = $shell.NameSpace($extractDir)
        if ($null -eq $zipFolder -or $null -eq $outputFolder) { throw 'Could not open the ZIP archive.' }
        $item = $zipFolder.ParseName('relaycat.exe')
        if ($null -eq $item) { throw 'ZIP archive does not contain relaycat.exe.' }
        $outputFolder.CopyHere($item, 16)
        $sourceExe = Join-Path $extractDir 'relaycat.exe'
        $complete = $false
        for ($i = 0; $i -lt 240; $i++) {
            if ((Test-Path $sourceExe) -and ((Get-Item $sourceExe).Length -eq $item.Size)) {
                $complete = $true
                break
            }
            Start-Sleep -Milliseconds 250
        }
        if (-not $complete) { throw 'Timed out extracting relaycat.exe.' }
    }

    $sourceExe = Join-Path $extractDir 'relaycat.exe'
    if (-not (Test-Path $sourceExe)) { throw 'ZIP archive does not contain relaycat.exe.' }
    & $sourceExe version | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Downloaded executable cannot run on this system.' }

    [IO.Directory]::CreateDirectory($InstallDir) | Out-Null
    $target = Join-Path $InstallDir 'relaycat.exe'
    $staged = Join-Path $InstallDir ('relaycat.new.' + [Guid]::NewGuid().ToString('N') + '.exe')
    [IO.File]::Copy($sourceExe, $staged)
    & $staged version | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Staged executable cannot run on this system.' }

    $backup = $null
    if ([IO.File]::Exists($target)) {
        $backup = Join-Path $InstallDir ('relaycat.backup.' + [Guid]::NewGuid().ToString('N') + '.exe')
        [IO.File]::Move($target, $backup)
    }
    try {
        [IO.File]::Move($staged, $target)
        $staged = $null
        & $target version | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Installed executable did not start.' }
    } catch {
        if ([IO.File]::Exists($target)) { [IO.File]::Delete($target) }
        if ($null -ne $backup) { [IO.File]::Move($backup, $target) }
        throw
    }
    if ($null -ne $backup) {
        try { [IO.File]::Delete($backup) } catch { Write-Warning "Previous binary retained at $backup" }
    }

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $pathEntries = @($userPath -split ';' | Where-Object { $_ -ne '' })
    $found = $false
    foreach ($entry in $pathEntries) {
        if ($entry.TrimEnd('\') -ieq $InstallDir.TrimEnd('\')) { $found = $true; break }
    }
    if (-not $found) {
        [Environment]::SetEnvironmentVariable('Path', (($pathEntries + $InstallDir) -join ';'), 'User')
    }
    if (($env:Path -split ';') -notcontains $InstallDir) { $env:Path += ";$InstallDir" }

    Write-Host "Installed $Version to $target"
    & $target version
    Write-Host 'Open a new terminal to use relaycat by name.'
} finally {
    if ($null -ne $staged -and [IO.File]::Exists($staged)) {
        try { [IO.File]::Delete($staged) } catch { Write-Warning "Could not remove temporary file $staged" }
    }
    if ($null -ne $tempDir -and [IO.Directory]::Exists($tempDir)) {
        try { [IO.Directory]::Delete($tempDir, $true) } catch { Write-Warning "Could not remove temporary directory $tempDir" }
    }
    $client.Dispose()
}
