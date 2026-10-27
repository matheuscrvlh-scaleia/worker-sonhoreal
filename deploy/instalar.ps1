<#
  Instala o worker-fdc nesta maquina. Rodar como Administrador, de dentro da pasta extraida do zip:

    powershell -ExecutionPolicy Bypass -File .\instalar.ps1 -TokenWorker <FDC_TOKEN_WORKER>

  1a vez: cria C:\worker-fdc\config.json com o token do worker (o que ja esta nos secrets da
  Edge Function; sem -TokenWorker, gera um novo e mostra o comando para cadastrar) e para,
  pedindo para conferir o banco. Se achar a instalacao antiga (C:\ponte), aproveita o banco
  dela e remove o servico PonteERP e o tunel Cloudflare, que nao sao mais usados.
  Depois de preencher, rode de novo: testa banco, Supabase e extracoes, instala o servico e
  confere o /health local.
  Rodar de novo outras vezes e seguro: troca os .exe e reinicia, sem mexer no config.json.
#>
param(
  [string]$Destino = "C:\worker-fdc",
  [string]$Antigo = "C:\ponte",
  [string]$TokenWorker = ""
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
$agente = Join-Path $Destino "worker-fdc-agente.exe"
$cfg = Join-Path $Destino "config.json"

# ---- 1. config.json (nunca sobrescreve)
if (-not (Test-Path $cfg)) {
  $c = Get-Content (Join-Path $origem "config.exemplo.json") -Raw | ConvertFrom-Json
  $c.supabase.token = if ($TokenWorker) { $TokenWorker.Trim() } else { NovoToken }
  $velho = Join-Path $Antigo "config.json"
  if (Test-Path $velho) {
    $c.banco = (Get-Content $velho -Raw | ConvertFrom-Json).banco
    Write-Host "Banco copiado da instalacao antiga ($velho)."
  }
  GravarSemBOM $cfg ($c | ConvertTo-Json -Depth 5)
  Write-Host ""
  Write-Host "config.json criado em $cfg" -ForegroundColor Yellow
  Write-Host "Agora:"
  if (-not $TokenWorker) {
    Write-Host "  - cadastre o token novo no Supabase (secret da Edge Function fdc):"
    Write-Host "       npx supabase secrets set FDC_TOKEN_WORKER=$($c.supabase.token) --project-ref mnhtuzsmwacvsymtbyck"
  }
  Write-Host "  - confira a secao 'banco' no config.json (e rode usuario_leitura.sql se for banco novo)"
  Write-Host "  - rode este script de novo"
  notepad $cfg
  exit 0
}

# ---- 2. testa a versao NOVA (desta pasta) no banco real ANTES de mexer no que esta rodando
Write-Host "Testando a versao nova (banco, Supabase e extracoes)..."
& (Join-Path $origem "worker-fdc.exe") -testar -config $cfg -queries (Join-Path $origem "queries.yaml")
if ($LASTEXITCODE -ne 0) {
  Write-Host "Teste falhou. NADA foi alterado: a versao que estava rodando continua no ar." -ForegroundColor Red
  Write-Host "Corrija (config.json, token no Supabase, queries.yaml ou GRANT no banco) e rode de novo."
  exit 1
}

# ---- 3. instalacao antiga (API + tunel Cloudflare): sai do ar, a IA passa a ler o Supabase
if (Get-Service PonteERP -ErrorAction SilentlyContinue) {
  Write-Host "Removendo o servico antigo PonteERP..."
  Stop-Service PonteERP -Force -ErrorAction SilentlyContinue
  sc.exe delete PonteERP | Out-Null
}
if (Get-Service cloudflared -ErrorAction SilentlyContinue) {
  Write-Host "Removendo o tunel Cloudflare..."
  Stop-Service cloudflared -Force -ErrorAction SilentlyContinue
  $cf = Join-Path $Antigo "cloudflared.exe"
  if (Test-Path $cf) {
    # O cloudflared escreve o log no stderr; com "Stop", o PowerShell 5 trata isso como erro e para.
    $ErrorActionPreference = "Continue"
    & $cf service uninstall 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"
  } else { sc.exe delete cloudflared | Out-Null }
}

# ---- 4. troca os arquivos (o servico fica parado so durante a copia)
$existia = [bool](Get-Service WorkerFDC -ErrorAction SilentlyContinue)
if ($existia) {
  Write-Host "Parando o servico WorkerFDC para trocar os arquivos..."
  & $agente parar | Out-Null
  Start-Sleep -Seconds 3
}
try {
  if ((Resolve-Path $origem).Path -ne (Resolve-Path $Destino).Path) {
    foreach ($f in "worker-fdc-agente.exe", "worker-fdc.exe", "queries.yaml", "config.exemplo.json", "usuario_leitura.sql", "instalar.ps1") {
      Copy-Item (Join-Path $origem $f) $Destino -Force
    }
  }
} finally {
  # ---- 5. servico (inicio automatico, reinicia sozinho se cair). Sobe mesmo se a copia falhar.
  if (-not $existia) { & $agente instalar }
  & $agente iniciar
}

# ---- 6. conferencia final
$escutar = (Get-Content $cfg -Raw | ConvertFrom-Json).local.escutar
Start-Sleep -Seconds 5
try {
  $h = Invoke-RestMethod "http://$escutar/health" -TimeoutSec 10
  Write-Host ""
  Write-Host "PRONTO. worker-fdc $($h.versao) no ar, banco_ok=$($h.banco_ok)." -ForegroundColor Green
  Write-Host "A primeira carga (clientes, fotos) leva alguns minutos. Acompanhe em $Destino\logs\worker.log"
  Write-Host "Status: $agente status"
} catch {
  Write-Host "Servico instalado, mas o /health nao respondeu. Veja $Destino\logs\agente.log" -ForegroundColor Red
  exit 1
}
