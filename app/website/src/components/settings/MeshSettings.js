// mesh 组网卡片纯逻辑模块（与 MeshSettings.vue 同名分层：.js=可纯测的归一化/
// 解析逻辑，.vue=卡片骨架；import 一律带显式扩展名避免同名解析歧义）。
// 线格式 customer.mesh 与后端 processor/config types.go MeshConfig 对齐：
// {enable, forward_enable, max_hops, peers:[{device_id,lan_addrs,relay}],
//  fallback:{enable,classes,addr}}。
//
// 三态布尔语义（定死，与后端 Effective* 同源）：
//   enable          nil → true   （mesh 是直连之上的增量出口，升级不许静默关）
//   forward_enable  nil → false  （被转发是让渡型能力，默认关）
//   peer.relay      nil → true   （LAN 不可达时允许走中继载体）
//   fallback.enable nil → false  （离线兜底默认关）
// 读侧恒产出显式布尔（ssh_ingress「恒产出显式」先例）：保存 patch 才不会
// 因缺键被后端 deep-merge 静默吞掉——⚠️ 不要把这里「改回」三态透传，那会
// 重现 peer_mcp「保存 200 但永不落盘」缺陷族。

// 兜底流量类别（后端 meshproto ClassAI/ClassSelf/ClassCustom；design §9.4
// 勾选序 = AI / 自身 / 自定义）。导出供卡片勾选框与测试共用，勿内联字面量。
export const MESH_FALLBACK_CLASSES = ['ai', 'self', 'custom'];

// 与后端 meshproto DefaultHops=5 / MaxHopsCap=8 对齐,语义=后端
// EffectiveMaxHops 读侧同款收口：≤0/非数归 0（=用后端默认 5）；>8 钳到 8
// （后端读侧对越界值的运行表现就是 8——归 0 会把 9 错读成默认 5）。钳制是
// 有意选择：越界值不透传给后端 Validate（错误框呈现留给 addr 这类用户
// 手输字段）；Task 3 若加 max_hops 手输框,输入校验在表单层自行拦截。
function normalizeMaxHops(raw) {
  if (typeof raw !== 'number' || !Number.isFinite(raw) || raw <= 0) {
    return 0;
  }
  if (raw > 8) {
    return 8;
  }
  return Math.floor(raw);
}

function normalizeMeshPeers(raw) {
  if (!Array.isArray(raw)) {
    return [];
  }
  const peers = [];
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      continue;
    }
    const deviceId = String(entry.device_id ?? '').trim();
    if (!deviceId) {
      continue; // 空白 device_id 是读侧垃圾（后端保存校验必拒），剔除
    }
    peers.push({
      device_id: deviceId,
      lan_addrs: Array.isArray(entry.lan_addrs)
        ? entry.lan_addrs.map((addr) => String(addr ?? '').trim()).filter(Boolean)
        : [],
      relay: typeof entry.relay === 'boolean' ? entry.relay : true
    });
  }
  return peers;
}

function normalizeMeshFallback(raw) {
  const source = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw : {};
  const classes = [];
  const rawClasses = Array.isArray(source.classes) ? source.classes : [];
  for (const item of rawClasses) {
    const value = String(item ?? '').trim();
    // 只认三个合法类别，规整 + 去重；后端 Validate 对非法类别整单拒绝，
    // 读侧剔除是防御手改配置文件漏进来的垃圾。
    if (MESH_FALLBACK_CLASSES.includes(value) && !classes.includes(value)) {
      classes.push(value);
    }
  }
  return {
    enable: typeof source.enable === 'boolean' ? source.enable : false,
    classes,
    addr: typeof source.addr === 'string' ? source.addr.trim() : ''
  };
}

// 线格式归一（customer.mesh）。SettingsPage 的 createDefaultCustomerConfig/
// normalizeCustomerConfig 链与 MeshSettings.vue 保存路径共用——⚠️ 漏挂
// normalizeCustomerConfig = 保存 200 但永不落盘（peer_mcp 验收缺陷①同族）。
export function normalizeMeshConfig(payload) {
  const source = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : {};
  return {
    enable: typeof source.enable === 'boolean' ? source.enable : true,
    forward_enable: typeof source.forward_enable === 'boolean' ? source.forward_enable : false,
    max_hops: normalizeMaxHops(source.max_hops),
    peers: normalizeMeshPeers(source.peers),
    fallback: normalizeMeshFallback(source.fallback)
  };
}

// —— mesh:<id> 尾段解析（Task 3 画布连线徽标消费；客户端与后端
// processor/routing splitMeshTail 语义对齐）——
const MESH_TAIL_PREFIX = 'mesh:';
const MESH_FALLBACK_KEYWORD = 'fallback';

// 策略段归一（后端 normalizeMeshPolicySegment 同语义）：direct|deny 关键字
// 大小写不敏感、归一小写；fallback:<id2> 关键字等值忽略大小写、目标
// TrimSpace 保留原大小写且非空。未知/空段 → null（非法）。
function normalizeMeshPolicySegment(segment) {
  const value = String(segment ?? '').trim();
  if (!value) {
    return null;
  }
  const lower = value.toLowerCase();
  if (lower === 'direct' || lower === 'deny') {
    return lower;
  }
  const keywordLength = MESH_FALLBACK_KEYWORD.length; // 'fallback'
  if (
    value.length > keywordLength + 1 &&
    value[keywordLength] === ':' &&
    value.slice(0, keywordLength).toLowerCase() === MESH_FALLBACK_KEYWORD
  ) {
    const target = value.slice(keywordLength + 1).trim();
    if (target) {
      return `${MESH_FALLBACK_KEYWORD}:${target}`;
    }
  }
  return null;
}

// splitMeshTail 客户端复刻：任一逗号段（TrimSpace+小写）以 mesh: 开头即
// mesh 条目；mesh: 段后至多一个策略段；deviceID（原大小写）与 base 均
// TrimSpace 非空。非法 → null（后端整条静默跳过，此处不计入）。
function splitMeshTail(raw) {
  const parts = String(raw).split(',');
  let meshIndex = -1;
  for (let i = 0; i < parts.length; i += 1) {
    if (parts[i].trim().toLowerCase().startsWith(MESH_TAIL_PREFIX)) {
      meshIndex = i;
      break;
    }
  }
  if (meshIndex < 0) {
    return null; // 非 mesh 条目（后端 meshTailNone，交回常规分类）
  }
  const tailParts = parts.slice(meshIndex + 1);
  if (tailParts.length > 1) {
    return null;
  }
  if (tailParts.length === 1 && normalizeMeshPolicySegment(tailParts[0]) === null) {
    return null;
  }
  const tail = parts[meshIndex].trim();
  const deviceId = tail.slice(MESH_TAIL_PREFIX.length).trim();
  if (!deviceId) {
    return null;
  }
  const base = parts.slice(0, meshIndex).join(',').trim();
  if (!base) {
    return null;
  }
  return { base, deviceId };
}

// proxy_rules 条目 → 各 mesh 目标节点的规则计数。Task 3 连线徽标（节点对
// 规则数）数据源；非法 mesh 条目与后端同口径不计入（不虚增徽标数字）。
export function parseMeshRuleTargetCount(rules) {
  if (!Array.isArray(rules)) {
    return {};
  }
  const counts = {};
  for (const rule of rules) {
    if (typeof rule !== 'string') {
      continue;
    }
    const parsed = splitMeshTail(rule);
    if (!parsed) {
      continue;
    }
    counts[parsed.deviceId] = (counts[parsed.deviceId] || 0) + 1;
  }
  return counts;
}

// proxy_rules 条目 → Map<deviceID, 规则原文[]>（Task 3 详情面板 chips 数据源）。
// 与 parseMeshRuleTargetCount 同一 splitMeshTail 口径:合法 mesh 条目才归入,
// 原文按输入顺序保序（chips 展示顺序 = 规则顺序）。
export function listMeshRulesByPeer(rules) {
  const grouped = new Map();
  if (!Array.isArray(rules)) {
    return grouped;
  }
  for (const rule of rules) {
    if (typeof rule !== 'string') {
      continue;
    }
    const parsed = splitMeshTail(rule);
    if (!parsed) {
      continue;
    }
    const bucket = grouped.get(parsed.deviceId);
    if (bucket) {
      bucket.push(rule);
    } else {
      grouped.set(parsed.deviceId, [rule]);
    }
  }
  return grouped;
}
