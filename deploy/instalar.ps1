<#
  Instala a Ponte ERP nesta maquina. Rodar como Administrador, de dentro da pasta extraida do zip:

    powershell -ExecutionPolicy Bypass -File .\instalar.ps1 -TokenTunel <token do tunel Cloudflare>

  1a vez: cria C:\ponte\config.json com tokens novos e para, pedindo para preencher o banco.
  Depois de preencher, rode de novo: testa as consultas, instala os servicos e confere o /health.
  Rodar de novo outras vezes e seguro: troca os .exe e reinicia, sem mexer no config.json.
#>
param(
  [string]$Destino = "C:\ponte",
  [string]$TokenTunel = ""
)
$ErrorActionPreference = "Stop"
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { throw "Rode este script como Administrador." }

function NovoToken {
  $b = New-Object byte[] 24
  [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
  -join ($b | ForEach-Object { $_.ToString("x2") })
}
function GravarSemBOM([string]$caminho, [string]$texto) {
  [IO.File]::WriteAllText($caminho, $texto, (New-Object Text.UTF8Encoding $false))
}

$origem = $PSScriptRoot
New-Item -ItemType Directory -Force $Destino | Out-Null
$agente = Join-Path $Destino "ponte-agente.exe"
$api = Join-Path $Destino "ponte-api.exe"
$cfg = Join-Path $Destino "config.json"

# ---- 1. arquivos
if (Get-Service PonteERP -ErrorAction SilentlyContinue) {
  Write-Host "Parando o servico PonteERP para trocar os arquivos..."
  & $agente parar | Out-Null
  Start-Sleep -Seconds 3
}
if ((Resolve-Path $origem).Path -ne (Resolve-Path $Destino).Path) {
  foreach ($f in "ponte-agente.exe", "ponte-api.exe", "queries.yaml", "config.exemplo.json", "usuario_leitura.sql", "instalar.ps1") {
    Copy-Item (Join-Path $origem $f) $Destino -Force
  }
}

# ---- 2. config.json (nunca sobrescreve)
if (-not (Test-Path $cfg)) {
  $c = Get-Content (Join-Path $Destino "config.exemplo.json") -Raw | ConvertFrom-Json
  $c.api.token = NovoToken
  $c.api.token_admin = NovoToken
  GravarSemBOM $cfg ($c | ConvertTo-Json -Depth 5)
  Write-Host ""
  Write-Host "config.json criado em $cfg" -ForegroundColor Yellow
  Write-Host "  token       : $($c.api.token)"
  Write-Host "  token_admin : $($c.api.token_admin)"
  Write-Host "Guarde os tokens (vao na configuracao da IA). Agora:"
  Write-Host "  1. rode usuario_leitura.sql no banco (instrucoes dentro do arquivo)"
  Write-Host "  2. preencha a secao 'banco' e a 'chave_publica' no config.json"
  Write-Host "  3. rode este script de novo"
  notepad $cfg
  exit 0
}

# ---- 3. teste real no banco antes de ligar qualquer coisa
Write-Host "Testando conexao e consultas..."
& $api -testar -config $cfg
if ($LASTEXITCODE -ne 0) {
  Write-Host "Teste falhou: corrija o config.json / queries.yaml e rode de novo." -ForegroundColor Red
  exit 1
}

# ---- 4. servico (inicio automatico, reinicia sozinho se cair)
if (-not (Get-Service PonteERP -ErrorAction SilentlyContinue)) { & $agente instalar }
& $agente iniciar

# ---- 5. tunel Cloudflare
if ($TokenTunel) {
  $cf = Join-Path $Destino "cloudflared.exe"
  if (-not (Test-Path $cf)) {
    Write-Host "Baixando cloudflared..."
    Invoke-WebRequest "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe" -OutFile $cf -UseBasicParsing
  }
  if (-not (Get-Service cloudflared -ErrorAction SilentlyContinue)) { & $cf service install $TokenTunel }
  sc.exe failure cloudflared reset= 86400 actions= restart/5000/restart/5000/restart/30000 | Out-Null
  Start-Service cloudflared -ErrorAction SilentlyContinue
}

# ---- 6. maquina nao pode dormir
powercfg /change standby-timeout-ac 0
powercfg /change hibernate-timeout-ac 0

# ---- 7. conferencia final
$escutar = (Get-Content $cfg -Raw | ConvertFrom-Json).api.escutar
Start-Sleep -Seconds 5
try {
  $h = Invoke-RestMethod "http://$escutar/health" -TimeoutSec 10
  Write-Host ""
  Write-Host "PRONTO. API $($h.versao) no ar, banco_ok=$($h.banco_ok), $($h.consultas) consultas." -ForegroundColor Green
  Write-Host "Logs: $Destino\logs   |   Status: $agente status"
} catch {
  Write-Host "Servico instalado, mas o /health nao respondeu. Veja $Destino\logs\agente.log" -ForegroundColor Red
  exit 1
}
