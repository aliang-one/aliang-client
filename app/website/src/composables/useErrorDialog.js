import { reactive } from 'vue';

// 全局报错弹窗单例(2026-11-17 用户定案:所有「操作失败/加载失败」类报错
// 反馈统一收口到这里,以模态弹窗替代散落各卡片的行内红字)。
// 最新一条覆盖上一条(latest-wins),不排队轰炸;成功/进行中反馈不入此通道。
const state = reactive({
  open: false,
  message: ''
});

export function showErrorDialog(text) {
  const value = typeof text === 'string' ? text : String(text ?? '');
  if (!value) {
    return;
  }
  state.message = value;
  state.open = true;
}

export function closeErrorDialog() {
  state.open = false;
  state.message = '';
}

// ErrorDialog.vue 订阅用;报错方只调 showErrorDialog,不触碰 state。
export function errorDialogState() {
  return state;
}
