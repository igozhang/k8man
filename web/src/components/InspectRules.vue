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
    <p class="inspect-rules-lead">巡检与诊断共用同一套定级。卡片、摘要、表格看的都是降级后的级别；提示不把集群刷成着火。占行看对象当前是否仍成立；Warning 事件默认只挂说明。Restarts 是例外：Pod 已 Ready 也会报。</p>

    <h3>四级（刚出现、未降级）</h3>
    <ul>
      <li>严重：kubeconfig 不通；节点 Ready≠True；负载 ready=0；Pod CrashLoop / ImagePull / CreateContainerError / OOMKilled / Failed（排除 Evicted 等残留）。</li>
      <li>错误：负载 0&lt;ready&lt;desired；节点 Disk/Memory/PID/Network 压力；Pod Running 未就绪或 Unknown；Pending Unschedulable 超过 2 分钟。</li>
      <li>警告：CPU/内存 ≥ 90%；DaemonSet 仅 misscheduled；其它 Pending 超过 15 分钟；Restarts；独立成行的调度/挂载失败。从严重/错误降下来的项也会落在警告。</li>
      <li>提示：不是新的事件类型，只作为能降级问题的终点。诊断表默认隐藏，勾选「显示提示」或筛选「提示」可见；监控摘要在仅有提示时写「N 提示」，卡片不当故障刷（预览也不含提示）。Restarts / 用量 / 压力 / kubeconfig / 独立事件不降级，到不了提示。</li>
    </ul>

    <h3>降级</h3>
    <ul>
      <li>问题连续成立满 30 天降 1 级，满 60 天再降 1 级，最低到提示。时钟用条件首次成立时间（Ready=False 的 LastTransitionTime），不跟每次重启刷新。</li>
      <li>例如普通命名空间的 ImagePullBackOff 基线是严重，满 60 天收敛为警告；系统命名空间最多降 1 级且不低于错误，不会落到警告/提示。</li>
      <li>不降级：kubeconfig、节点 CPU/内存用量、节点压力、Restarts、独立事件。Restarts 满 30 天从列表消失，不会变成提示。</li>
      <li>节点 NotReady 要降；Ready 节点为 0 或未就绪 ≥ 半数时整组不降。</li>
      <li>仍在 CrashLoop / OOM 的，降级下限是错误。kube-system / kube-public / kube-node-lease / istio-system / istio-operator / cattle-* / calico-* 最多降 1 级且不低于错误（不会把警告抬成错误）。</li>
    </ul>

    <h3>怎么算</h3>
    <ul>
      <li>节点 Ready≠True → 严重；Disk/Memory/PID/Network 压力为 True → 错误；CPU/内存用量 ≥ 90% → 警告。项目凭证不扫节点。用量依赖 metrics-server，采不到则跳过、不报错。</li>
      <li>负载 ready &lt; desired 且不是滚动中 → 副本为 0 严重，否则错误。DaemonSet misscheduled &gt; 0 → 警告。缩到 0 不算。</li>
      <li>滚动中（Progressing=True 且 ReplicaSetUpdated 等，或 Unavailable&gt;0 且 Progressing 持续不足 15 分钟）不算缺口；超过 15 分钟仍对不上才报。</li>
      <li>Pod 正在 CrashLoop / 拉镜像失败 / CreateContainerError / OOMKilled → 严重。Failed（非 Evicted/Shutdown 等）→ 严重。Unknown、Running 未就绪 → 错误。</li>
      <li>Pending Unschedulable 超过 2 分钟 → 错误；其它 Pending 超过 15 分钟 → 警告。Succeeded、刚创建的 Pending 不算。</li>
      <li>Restarts：当前 Ready 的 Pod，计入次数 ≥ 3 且上次异常退出在 30 天内 → 警告。计入次数看容器 <code>restartCount</code>，但上次退出若是 Completed / Success / exit=0 则该容器按 0。上次超过 30 天、或计入不足 3，不占行。上次退出时间缺失时，只要计入 ≥ 3 仍占行。</li>
      <li>只报 Pod 行。控制器已 Ready 时不另占负载 Restarts；已有 NotReady 时只把重启时间线挂到该行说明。卡片按行计数，同一负载不会算两遍。</li>
      <li>说明里的「近 2h 内有/无重启」与「计入重启 N 次」：前者只描述最近 2 小时；后者是 Completed/exit=0 剔除后的计入次数。占行门槛是上次异常退出 ≤ 30 天，不是近 2h。</li>
    </ul>

    <h3>说明里的最近重启</h3>
    <ul>
      <li>尽力列出最近 3 次<strong>异常退出</strong>：优先 <code>status.lastState</code>（API 只保留上次），其余来自 Warning 的 Killing / BackOff。Completed / exit=0 不列入。</li>
      <li>凑不满 3 条会写 restartNote，不编造时间。事件常被聚合成一条 BackOff ×N，只能确认 lastSeen。</li>
    </ul>

    <h3>算事件</h3>
    <ul>
      <li>Warning 优先挂到已有异常的说明（每条最多 5 条），不重复占行。</li>
      <li>独立成行仅当：lastSeen 在 2 小时内，且原因是 FailedScheduling / FailedMount / FailedAttachVolume / FailedCreate / FailedBinding，或 BackOff、含 OOM 的 Killing、对象仍不健康的 Evicted。</li>
    </ul>

    <h3>明确不算</h3>
    <ul>
      <li>Unhealthy / ProbeWarning：kubelet 每次探针失败都记，Pod 仍 Ready 表示未达 failureThreshold。</li>
      <li>对象当前 Ready 时，Warning 事件不独立占行。这不是「Ready 就不报警告」：Restarts 仍可占行。</li>
      <li>lastSeen 超过 2 小时的历史事件。</li>
      <li>Evicted / Shutdown 等已被 kubelet 清掉的 Failed 残留。</li>
      <li>滚动宽限期内的副本缺口。</li>
    </ul>
  </details>
</template>
