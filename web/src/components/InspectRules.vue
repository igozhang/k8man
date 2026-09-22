<script setup>
import { ref } from 'vue'

defineProps({
  title: { type: String, required: true }
})

const root = ref(null)
function setOpen(open) {
  if (root.value) root.value.open = !!open
}
defineExpose({ setOpen })
</script>

<template>
  <details ref="root" class="inspect-rules" open>
    <summary>{{ title }}</summary>
    <p class="inspect-rules-lead">
      「巡检」摘要与「诊断」表格共用同一套规则：先按对象当前状态给出<strong>基线级别</strong>，再按问题已持续多久做<strong>时间降级</strong>。页面上看到的都是降级后的级别；仅「提示」不会把集群刷成故障态。占行只看问题现在是否仍成立；Warning 事件默认挂到已有行的说明里。Restarts 是例外：Pod 已 Ready 也可单独占行。
    </p>

    <h3>一、基线四级（刚出现、尚未降级）</h3>
    <ul>
      <li><strong>严重：</strong>kubeconfig 不通；节点 Ready≠True；负载 ready=0；Pod CrashLoop / ImagePull / CreateContainerError / OOMKilled / Failed（排除 Evicted 等残留）；NFS 使用率 ≥ 80%。</li>
      <li><strong>错误：</strong>负载 0&lt;ready&lt;desired；节点 Disk/Memory/PID/Network 压力；Pod Running 未就绪或 Unknown；Pending Unschedulable 超过 2 分钟。</li>
      <li><strong>警告：</strong>CPU/内存 ≥ 90%；DaemonSet 仅 misscheduled；其它 Pending 超过 15 分钟；Restarts；独立成行的调度/挂载失败；大 POD（非持久化 &gt;5Gi）。从严重/错误降下来的项也会落在警告。</li>
      <li><strong>提示：</strong>不是新的事件类型，而是可降级问题的终点。诊断表默认隐藏（勾选「显示提示」或筛「提示」可见）；巡检摘要在仅有提示时写「N 提示」，不当故障成块。kubeconfig / 用量 / 压力 / Restarts / 独立事件 / 大 POD / NFS 使用率不降级，到不了提示。</li>
    </ul>

    <h3>二、时间降级</h3>
    <ul>
      <li>同一问题连续成立满 <strong>30 天降 1 级</strong>，满 <strong>60 天再降 1 级</strong>，最低到提示。时钟用条件首次成立时间（如 Ready=False 的 LastTransitionTime），不随每次重启刷新。</li>
      <li>例：普通命名空间 ImagePullBackOff 基线为严重 → 满 60 天可收敛为警告；系统命名空间最多降 1 级且不低于错误，不会落到警告/提示。</li>
      <li><strong>不降级：</strong>kubeconfig、节点 CPU/内存用量、节点压力、Restarts、独立事件、大 POD、NFS 使用率。其中 Restarts 满 30 天从列表消失，不会变成提示。</li>
      <li>节点 NotReady 可降；Ready 节点为 0 或未就绪 ≥ 半数时，该组节点问题整组不降。</li>
      <li>仍在 CrashLoop / OOM 的，降级下限是错误。kube-system / kube-public / kube-node-lease / istio-system / istio-operator / cattle-* / calico-* 最多降 1 级且不低于错误。</li>
    </ul>

    <h3>三、各类对象怎么算</h3>
    <ul>
      <li><strong>节点：</strong>Ready≠True → 严重；Disk/Memory/PID/Network 压力为 True → 错误；CPU/内存用量 ≥ 90% → 警告。Project 凭证不扫节点。用量依赖 metrics-server，采不到则跳过、不报错。</li>
      <li><strong>负载：</strong>ready &lt; desired 且非滚动中 → 副本为 0 严重，否则错误。DaemonSet misscheduled &gt; 0 → 警告。缩到 0 不算。滚动中（Progressing=True 且 ReplicaSetUpdated 等，或 Unavailable&gt;0 且 Progressing 不足 15 分钟）不算缺口；超过 15 分钟仍对不上才报。</li>
      <li><strong>Pod：</strong>CrashLoop / 拉镜像失败 / CreateContainerError / OOMKilled → 严重；Failed（非 Evicted/Shutdown 等）→ 严重；Unknown、Running 未就绪 → 错误。Pending Unschedulable 超过 2 分钟 → 错误；其它 Pending 超过 15 分钟 → 警告。Succeeded、刚创建的 Pending 不算。</li>
      <li><strong>Restarts（警告）：</strong>当前 Ready 的 Pod，计入次数 ≥ 3 且上次异常退出在 30 天内才占行。计入看容器 <code>restartCount</code>，上次退出若 Completed / Success / exit=0 则该容器按 0。超过 30 天或计入不足 3 不占行；退出时间缺失时，计入 ≥ 3 仍占行。只报 Pod 行：控制器已 Ready 不另占负载 Restarts；已有 NotReady 时只把重启时间线挂到该行说明。</li>
      <li>说明里的「近 2h 内有/无重启」只描述最近 2 小时；「计入重启 N 次」是剔除 Completed/exit=0 后的次数。占行门槛是上次异常退出 ≤ 30 天，不是近 2h。</li>
      <li><strong>存储 · 大 POD（警告）：</strong>读 kubelet <code>stats/summary</code>：pod 级 <code>ephemeral-storage</code> &gt; 0 时用它；否则用容器 <code>rootfs</code>+<code>logs</code> 与非 PVC volume 之和。超过 <strong>5Gi</strong> 为警告。依赖 nodes/proxy；采不到则显示「—」。不降级。</li>
      <li><strong>存储 · NFS 使用率（≥80% 严重）：</strong>仅探测 NFS 形式 StorageClass：provisioner 含 <code>nfs</code>、或 <code>fuseim.pri/ifs</code>、或 SC 名含 <code>nfs</code>（排除 <code>non-nfs</code> 等，且不覆盖 local-path / ceph 等明确非 NFS 驱动）。优先取<strong>默认 SC</strong>；若无默认且集群<strong>仅有一个 SC</strong>则用该唯一 SC。用量优先读 kubelet volume stats，回退对挂载该 SC 的 Running Pod 做一次短超时 <code>df -P</code>。端点（服务器 IP/主机名 + 导出路径）从 SC parameters（含 <code>server</code>/<code>host</code>/<code>share</code>/<code>baseDir</code> 等）、SC/PV <code>mountOptions</code> 的 <code>addr=</code>、Bound PV 的 <code>spec.nfs</code>/<code>csi.volumeAttributes</code>，或 <code>df</code> 的 Filesystem 字段解析；summary 有用量但缺端点且剩余预算足够时会再短超时 df 补全。非法占位（如 <code>0.0.0.0</code>）会被丢弃。端点写入说明、证据与 <code>nodeIP</code>（复用为 NFS 服务器）。&lt;80% 只在概览染色、不占巡检行；≥80% 严重、不降级。非 NFS / 多 SC 无默认 / 无挂载 PVC 时显示「—」。</li>
    </ul>

    <h3>四、事件怎么挂</h3>
    <ul>
      <li>Warning 优先挂到已有异常的说明（每条最多 5 条），不重复占行。</li>
      <li>独立成行仅当：lastSeen 在 2 小时内，且原因是 FailedScheduling / FailedMount / FailedAttachVolume / FailedCreate / FailedBinding，或 BackOff、含 OOM 的 Killing、对象仍不健康的 Evicted。</li>
      <li>说明里最近重启：尽力列最近 3 次异常退出（优先 <code>status.lastState</code>，其余来自 Warning 的 Killing / BackOff）；Completed / exit=0 不列入。凑不满会写 restartNote；事件常聚合成 BackOff ×N，只能确认 lastSeen。</li>
    </ul>

    <h3>五、明确不算</h3>
    <ul>
      <li>Unhealthy / ProbeWarning：kubelet 每次探针失败都记，Pod 仍 Ready 表示未达 failureThreshold。</li>
      <li>对象当前 Ready 时，Warning 事件不独立占行（Restarts 仍可占行）。</li>
      <li>lastSeen 超过 2 小时的历史事件；Evicted / Shutdown 等已被清掉的 Failed 残留；滚动宽限期内的副本缺口。</li>
    </ul>
  </details>
</template>
