# 用最低优先级（Idle）跑一条命令，并把它的退出码原样带出来。整套 go test 很吃 CPU，合进 main 前用它跑（AGENTS「验收」）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/run-low.ps1 go test ./...
# 不用 `cmd /c start /low /wait /b ...`：它不管里面成没成功都返回 0（2026-10-25 实测 `exit 3` 也是 0），红灯会被当成过。
# 参数经 Start-Process -ArgumentList 传，带空格或引号的参数会被拆开；复杂命令先写成 .cmd / .ps1 文件再交给它。
# 输出是子进程直接写的、不经这里攒；go test 不加 -v 时按包攒，要看进度用 -json 写文件。
# 优先级在进程起来后马上设；之后它再起的子进程都继承 Idle（CreateProcess 的规则），起来前那几毫秒是普通优先级。
# 叫它的进程没了（后台任务被停、外层脚本被杀），整串一起收掉：命令放进"关作业就杀全部成员"的作业对象，
# 每秒看一眼父进程还在不在。Windows 杀进程不带子进程，不这样做 go test 和测试程序会没人收结果地空跑（2026-10-29）。
# 同一个作业对象再加内存上限（2026-11-17）：单个进程默认 8 GB、整串默认 24 GB，超了的进程再要内存就被拒（Go 报 out of memory 退出，测试变红），
# 不再把整台电脑拖到只剩 1 GB。当晚 linkcard、chatui 的测试因为反复注册字体各涨到十几二十几 GB，主人桌面崩了几次。
# 环境变量 SUMIKA_TEST_PROC_MEM_GB、SUMIKA_TEST_JOB_MEM_GB 改上限，0 表示不设。跑的时候到上限九成五就整串收掉（退出码 137；分配失败后 Go 有时卡住不退）；跑完到八成的明说，超过 4 GB 的提醒。
if ($args.Count -eq 0) { Write-Error '用法：run-low.ps1 命令 [参数…]'; exit 2 }

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class RunLowJob {
    [StructLayout(LayoutKind.Sequential)]
    struct BasicLimit { public long PerProcessUserTimeLimit, PerJobUserTimeLimit; public uint LimitFlags; public UIntPtr MinimumWorkingSetSize, MaximumWorkingSetSize; public uint ActiveProcessLimit; public UIntPtr Affinity; public uint PriorityClass, SchedulingClass; }
    [StructLayout(LayoutKind.Sequential)]
    struct IoCounters { public ulong a, b, c, d, e, f; }
    [StructLayout(LayoutKind.Sequential)]
    struct ExtendedLimit { public BasicLimit Basic; public IoCounters Io; public UIntPtr ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed; }
    [DllImport("kernel32.dll", SetLastError = true)] static extern IntPtr CreateJobObject(IntPtr a, string name);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool SetInformationJobObject(IntPtr job, int cls, ref ExtendedLimit info, uint len);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool AssignProcessToJobObject(IntPtr job, IntPtr proc);
    [DllImport("kernel32.dll", SetLastError = true)] static extern bool QueryInformationJobObject(IntPtr job, int cls, ref ExtendedLimit info, uint len, IntPtr ret);
    [DllImport("kernel32.dll", SetLastError = true)] public static extern bool CloseHandle(IntPtr h);
    // 建一个"最后一个句柄关掉就杀全部成员"的作业，把 proc 放进去；失败返回 IntPtr.Zero（照旧跑，只是没有这层保护）。
    // procLimit、jobLimit 是字节数，0 表示不设：单个进程、整串提交内存的上限，超了再申请就失败
    public static IntPtr Wrap(IntPtr proc, ulong procLimit, ulong jobLimit) {
        IntPtr job = CreateJobObject(IntPtr.Zero, null);
        if (job == IntPtr.Zero) return IntPtr.Zero;
        var info = new ExtendedLimit();
        info.Basic.LimitFlags = 0x2000; // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        if (procLimit > 0) { info.Basic.LimitFlags |= 0x100; info.ProcessMemoryLimit = (UIntPtr)procLimit; } // JOB_OBJECT_LIMIT_PROCESS_MEMORY
        if (jobLimit > 0) { info.Basic.LimitFlags |= 0x200; info.JobMemoryLimit = (UIntPtr)jobLimit; }       // JOB_OBJECT_LIMIT_JOB_MEMORY
        if (!SetInformationJobObject(job, 9, ref info, (uint)Marshal.SizeOf(typeof(ExtendedLimit))) || !AssignProcessToJobObject(job, proc)) {
            CloseHandle(job);
            return IntPtr.Zero;
        }
        return job;
    }
    // 作业里单个进程、整串的提交内存峰值（字节）；读不到返回 0
    public static ulong[] Peaks(IntPtr job) {
        var info = new ExtendedLimit();
        if (job == IntPtr.Zero || !QueryInformationJobObject(job, 9, ref info, (uint)Marshal.SizeOf(typeof(ExtendedLimit)), IntPtr.Zero)) return new ulong[] { 0, 0 };
        return new ulong[] { (ulong)info.PeakProcessMemoryUsed, (ulong)info.PeakJobMemoryUsed };
    }
}
'@

$rest = @()
if ($args.Count -gt 1) { $rest = $args[1..($args.Count - 1)] }

# go test 分两档排队（ADR-320，替代原来的两个通用名额）：环境变量 SUMIKA_TEST_LANE=verify|dev，默认 dev。
#   verify 档：合并队列、推送钩子、每日整套用，全机一个名额、独占；go-verify.ts 的 runLow 自己带上这个变量。
#   dev 档：开发中的代理和会话用，全机一个名额；参数里没有 -p 时自动加 -p N，N 是逻辑核数的一半（至少 1），免得一个 go test 把所有核铺满。
# 两档是两把不同名字的互斥锁，所以同时最多一个验证加一个开发测试。持有的进程被杀时系统自动放开，
# 下一个等的人拿到时会收到"被遗弃"异常，当作拿到了（计数信号量做不到这点，进程被杀名额就永久少一个）。
# 以前靠各自先数进程再开跑，几路同时数到"不到两个"就一起起跑，CPU 压到 90% 以上，app 包 20 分钟都跑不完（2026-11-08）。
# bun test 也排同一把锁（2026-11-12）：server/ 整套两三分钟，以前不经这里，推送钩子的 bun test 会和队列的 go test 叠着跑。
# 锁的名字沿用 sumika-go-test-<档>，新旧版本的 run-low 互相也排队。程序名按文件名认（go、go.exe、D:\...\bun.exe 都算）。
# 内存上限（GB，可带小数；0 不设）：读不懂、不是有限数、不在 0 或 0.1～4096 之间都报错退出，不悄悄不设。
# 放在抢名额之前：写错了马上报，不先排队、不持锁退出
function Read-MemGB([string]$name, [double]$def) {
    $v = [Environment]::GetEnvironmentVariable($name)
    if ([string]::IsNullOrWhiteSpace($v)) { return $def }
    $d = 0.0
    $ok = [double]::TryParse($v, [Globalization.NumberStyles]::Float, [Globalization.CultureInfo]::InvariantCulture, [ref]$d)
    if (-not $ok -or [double]::IsNaN($d) -or [double]::IsInfinity($d) -or ($d -ne 0 -and ($d -lt 0.1 -or $d -gt 4096))) {
        Write-Error "run-low：$name 要是 0（不设）或 0.1～4096 之间的数（GB），收到 '$v'"; exit 2
    }
    return $d
}
$procGB = Read-MemGB 'SUMIKA_TEST_PROC_MEM_GB' 8
$jobGB = Read-MemGB 'SUMIKA_TEST_JOB_MEM_GB' 24
$procLimit = [uint64]($procGB * 1GB)
$jobLimit = [uint64]($jobGB * 1GB)

$slot = $null
$tool = [IO.Path]::GetFileNameWithoutExtension([string]$args[0]).ToLowerInvariant()
if (($tool -eq 'go' -or $tool -eq 'bun') -and $rest.Count -gt 0 -and $rest[0] -eq 'test') {
    $lane = if ($env:SUMIKA_TEST_LANE) { $env:SUMIKA_TEST_LANE } else { 'dev' }
    if ($lane -ne 'verify' -and $lane -ne 'dev') { Write-Error "run-low：SUMIKA_TEST_LANE 只认 verify 或 dev，收到 '$lane'"; exit 2 }
    if ($tool -eq 'go' -and $lane -eq 'dev' -and -not ($rest | Where-Object { $_ -match '^--?p(=|$)' })) {
        $n = [Math]::Max(1, [int][Math]::Floor([Environment]::ProcessorCount / 2))
        $rest = @($rest[0], '-p', "$n") + @($rest | Select-Object -Skip 1)
    }
    # 祖先已经拿着这一档的名额（环境变量往下传）：不再抢。检查脚本的测试本身在名额里跑，测试里又经这里起 bun test，
    # 再抢同一把锁会永远等外层放开（锁按线程归属，别的进程进不去）
    $held = @(([string]$env:SUMIKA_SLOT_HELD) -split ',' | Where-Object { $_ })
    if ($held -contains $lane) { $lane = $null }
}
if ($lane) {
    # SUMIKA_SLOT_NS 只给 run-low 自己的测试用：锁名加后缀，和全机真实名额分开（2026-11-12：测试借 dev 档，
    # 有代理正占着 dev 档时三条测试各等满 60 秒、合并队列红了）。只认字母数字和横线，别的字符当没给
    $ns = [string]$env:SUMIKA_SLOT_NS
    $suffix = if ($ns -match '^[A-Za-z0-9-]{1,40}$') { "-$ns" } else { '' }
    $mutex = [System.Threading.Mutex]::new($false, "Local\sumika-go-test-$lane$suffix")
    $waited = $false
    while ($null -eq $slot) {
        try {
            if ($mutex.WaitOne(60000)) { $slot = $mutex }
        } catch [System.Threading.AbandonedMutexException] {
            $slot = $mutex
        }
        if ($null -eq $slot -and -not $waited) { Write-Host "run-low：$lane 档已经有一个测试在跑，排队等名额"; $waited = $true }
    }
    $env:SUMIKA_SLOT_HELD = (@($held) + @($lane)) -join ','   # 子进程继承：它们再经这里起测试时不重复抢
}

$p = if ($rest.Count) { Start-Process -FilePath $args[0] -ArgumentList $rest -NoNewWindow -PassThru } else { Start-Process -FilePath $args[0] -NoNewWindow -PassThru }
$null = $p.Handle   # 先拿住句柄，不然进程结束后读不到 ExitCode
$job = [RunLowJob]::Wrap($p.Handle, $procLimit, $jobLimit)
if ($job -eq [IntPtr]::Zero) { Write-Warning 'run-low：放不进作业对象，外层被停时子进程不会跟着停' }
try { $p.PriorityClass = 'Idle' } catch { }

$parentId = (Get-CimInstance Win32_Process -Filter "ProcessId=$PID").ParentProcessId
$parent = Get-Process -Id $parentId -ErrorAction SilentlyContinue
# 内存分配失败以后 Go 进程有时卡住不退出、一直占着内存和名额（复核实测卡过五分钟以上），
# 所以不只靠分配失败：每秒看一次峰值，到上限九成五就整串收掉，退出码 137
$gb = { param($b) '{0:N1}' -f ($b / 1GB) }
while (-not $p.WaitForExit(1000)) {
    if ($job -ne [IntPtr]::Zero -and ($procLimit -gt 0 -or $jobLimit -gt 0) -and -not $p.HasExited) {
        $peak = [RunLowJob]::Peaks($job)
        $hitProc = $procLimit -gt 0 -and $peak[0] -ge [uint64]($procLimit * 0.95)
        $hitJob = $jobLimit -gt 0 -and $peak[1] -ge [uint64]($jobLimit * 0.95)
        if ($hitProc -or $hitJob) {
            $what = if ($hitProc) { "单进程内存上限 $procGB GB（峰值 $(& $gb $peak[0]) GB）" } else { "整串内存上限 $jobGB GB（峰值 $(& $gb $peak[1]) GB）" }
            Write-Warning "run-low：碰到了$what，整串收掉。多半是测试在泄漏内存，go test 这时还没输出，加 -v 或按包单跑找是哪个测试"
            [RunLowJob]::CloseHandle($job) | Out-Null
            if ($slot) { $slot.ReleaseMutex() }
            exit 137
        }
    }
    if ($parent -and $parent.HasExited) {
        Write-Warning "run-low：叫它的进程（$parentId）已经退出，收掉整串"
        if ($job -ne [IntPtr]::Zero) { [RunLowJob]::CloseHandle($job) | Out-Null } else { $p.Kill() }
        if ($slot) { $slot.ReleaseMutex() }
        exit 130
    }
}
$p.WaitForExit()
$code = $p.ExitCode
if ($job -ne [IntPtr]::Zero) {
    $peak = [RunLowJob]::Peaks($job)
    $verb = if ($code -ne 0) { '碰到了上限、申请内存被拒' } else { '接近上限' }
    if ($procLimit -gt 0 -and $peak[0] -ge [uint64]($procLimit * 0.8)) {
        Write-Warning "run-low：单个进程内存峰值 $(& $gb $peak[0]) GB，$verb（单进程上限 $procGB GB）。多半是测试在泄漏内存，看上面哪个包 FAIL、报 out of memory"
    } elseif ($jobLimit -gt 0 -and $peak[1] -ge [uint64]($jobLimit * 0.8)) {
        Write-Warning "run-low：整串内存峰值 $(& $gb $peak[1]) GB，$verb（整串上限 $jobGB GB）"
    } elseif ($peak[0] -gt 4GB) {
        Write-Host "run-low：单个进程内存峰值 $(& $gb $peak[0]) GB（上限 $procGB GB），偏大，留意是不是在泄漏"
    }
    [RunLowJob]::CloseHandle($job) | Out-Null
}
if ($slot) { $slot.ReleaseMutex() }
exit $code
