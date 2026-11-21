// 事件监听器面板的纯格式化工具(specs/005-event-listener;面板=Task 16 消费)。
// 全部纯函数:i18n 经 t-as-arg 注入(仓内既定的可测 i18n 手法),时间锚点
// now 可注入,不 import 任何 Vue/网络代码。

// 云端离线哨兵:后端 agent 侧 REST 代理在云端不可达时回 502,body 形如
// {code:"listener_cloud_offline", msg:"listener cloud unreachable …"}
// (app/http/services/agent_listener_api.go writeListenerCloudError)。
// 注意:agentApi 的 request() 抛出的 Error 只带 envelope.msg(云端 prose 文案),
// 不带 code——见 isListenerCloudOffline 的双匹配说明。
export const LISTENER_CLOUD_OFFLINE = 'listener_cloud_offline';

// request() 现行抛出的 msg prose 稳定片段(与后端 writeListenerCloudError 同仓同批,
// 变更面共同评审;哨兵落进 msg 之日起上方常量匹配自动接管)。
const CLOUD_OFFLINE_PROSE_MARKER = 'listener cloud unreachable';

// 状态 → 徽标色调映射(语义自洽版):
//   active  → ok     在岗监听中(绿色=健康常驻态)
//   paused  → muted  用户暂停,惰性(灰)
//   fired   → warn   已触发:once 型到此终态待「复活」、recurring 型冷却中,
//                    值得用户注意(琥珀)
//   expired → muted  过期终态,惰性(灰)
//   error   → error  异常(红),行尾带 error_note,可复活
//   未知    → muted + listener_status_unknown 兜底
const STATUS_TONES = {
  active: 'ok',
  paused: 'muted',
  fired: 'warn',
  expired: 'muted',
  error: 'error',
};

export function listenerStatusBadge(status) {
  const known = typeof status === 'string' && Object.prototype.hasOwnProperty.call(STATUS_TONES, status);
  return {
    key: `listener_status_${known ? status : 'unknown'}`,
    tone: known ? STATUS_TONES[status] : 'muted',
  };
}

// 相对时间(档位与仓内 ChatMessageBubble.relativeTime 同构,多一档「天」):
//   <60s 刚刚(未来时间戳钳为刚刚,防时钟偏移)/ <60min X 分钟前 /
//   <24h X 小时前 / <7d X 天前 / 更早 M-D HH:mm 日期串。
// 缺值/非法输入返回 ''(由调用方渲染占位符)。
export function formatRelativeTime(isoString, now, t) {
  const ms = Date.parse(isoString);
  if (!Number.isFinite(ms)) return '';
  const diff = Math.max(0, (typeof now === 'number' ? now : Date.now()) - ms);
  if (diff < 60 * 1000) return t('listener_justNow');
  if (diff < 60 * 60 * 1000) return t('listener_minutesAgo', { minutes: Math.floor(diff / 60000) });
  if (diff < 24 * 60 * 60 * 1000) return t('listener_hoursAgo', { hours: Math.floor(diff / 3600000) });
  if (diff < 7 * 24 * 60 * 60 * 1000) return t('listener_daysAgo', { days: Math.floor(diff / 86400000) });
  const d = new Date(ms);
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  return `${d.getMonth() + 1}-${d.getDate()} ${hh}:${mm}`;
}

// 列表行 → 展示就绪字段:
//   title        缺 title 兜底 id,再缺为空串
//   statusLine   「状态 · 模式」;error 行把 error_note 追加到末尾;
//                未知模式裸串透传,缺模式只留状态
//   lastFiredText 相对时间;从未触发(缺值/非法)渲染 —
// now 仅测试注入用,缺省 Date.now()。
export function formatListenerRow(row, t, now = Date.now()) {
  const src = row || {};
  const badge = listenerStatusBadge(src.status);
  const parts = [t(badge.key)];
  if (src.mode === 'once' || src.mode === 'recurring') {
    parts.push(t(`listener_mode_${src.mode}`));
  } else if (src.mode) {
    parts.push(String(src.mode));
  }
  if (src.status === 'error' && src.error_note) {
    parts.push(String(src.error_note));
  }
  return {
    title: src.title || src.id || '',
    statusLine: parts.join(' · '),
    lastFiredText: formatRelativeTime(src.last_fired_at, now, t) || '—',
  };
}

// 离线判定(双匹配,勿删任一):
//   ① prose 片段「listener cloud unreachable」——现行真实信号:request() 抛
//     Error(envelope.msg),而 502 body 的哨兵在 code 字段里被 extractEnvelope
//     丢弃(string code → 0),msg 是云端 prose;
//   ② 哨兵 LISTENER_CLOUD_OFFLINE 子串——后端把 code 透进 msg(或 request()
//     开始携带 code)之日起自动接管,面板无需改动。
export function isListenerCloudOffline(err) {
  const msg = err && typeof err.message === 'string' ? err.message : '';
  if (!msg) return false;
  return msg.includes(LISTENER_CLOUD_OFFLINE) || msg.includes(CLOUD_OFFLINE_PROSE_MARKER);
}

// 列表归一:云端已有 FR-11 空列表归一,这里前端再兜一层(缺值/非数组 → []),
// 保证面板可直接迭代。
export function normalizeListenerList(data) {
  return Array.isArray(data) ? data : [];
}
