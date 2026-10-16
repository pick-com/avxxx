// zh-CN strings for the custom player controls (control bar, progress
// slider, hover preview, buffering / error / resume / end states, keyboard
// hints) and the mobile mini player (MobileHome M1). Spread into zhCN in
// messages.zh-CN.ts; same key rules.

export const playerZhCN = {
  // control bar
  'pc.controls': '播放控件',
  'pc.seek': '播放进度',
  'pc.seekValue': '{time} / {duration}',
  'pc.seekValueChapter': '{time} / {duration}，{chapter}',
  'pc.volume': '音量',
  'pc.volumeValue': '{n}%',
  'pc.mute': '静音 (M)',
  'pc.unmute': '取消静音 (M)',
  'pc.showRemaining': '显示剩余时间',
  'pc.showElapsed': '显示已播放时间',
  'pc.settings': '设置',
  'pc.captionsOn': '打开字幕 (C)',
  'pc.captionsOff': '关闭字幕 (C)',
  'pc.chapter': '第 {n} 章 {title}',
  'pc.playKey': '播放 (空格)',
  'pc.pauseKey': '暂停 (空格)',

  // states
  'pc.buffering': '正在缓冲',
  'pc.offlineBuffered': '网络已断开，正在播放已缓冲的部分',
  'pc.offlineWaiting': '网络已断开，恢复后自动继续',
  'pc.switchSource': '换片源',
  'pc.resumed': '从 {time} 继续播放',
  'pc.startOver': '从头播放',
  'pc.replay': '重新播放',
  'pc.endTitle': '接下来看',
  'pc.closeToast': '关闭提示',

  // keyboard hints
  'pc.jumpTo': '跳到 {n}%',
  'pc.frameForward': '下一帧',
  'pc.frameBack': '上一帧',
  'pc.rateTo': '倍速 {rate}',
  'pc.toStart': '回到开头',
  'pc.toEnd': '跳到结尾',
  'pc.keysHint': '空格/K 播放/暂停 · ← → 5 秒 · J L 10 秒 · ↑ ↓ 音量 · 0–9 跳转 · , . 逐帧 · < > 倍速 · F 全屏 · T 影院模式 · I 画中画 · M 静音 · C 字幕 · Shift+N/P 下一部/上一部',

  // mini player (MobileHome M1)
  'mini.label': '迷你播放器',
  'mini.expand': '展开播放器：{title}',
  'mini.close': '关闭迷你播放器',
} as const;
