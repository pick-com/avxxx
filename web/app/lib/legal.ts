// Legal documents (LegalPage, PRD 15). The real texts are drafts awaiting
// counsel; every page here is a template with placeholder text and is marked
// [待律师审阅]. Nothing below is legal advice or a final policy.
// TODO(tech plan 3.13 legal_documents): load versioned, multi-language texts
// from the API (history, English version, effective dates).

export type LegalSection = {id: string; title: string; body: string};
export type LegalDoc = {slug: string; title: string; summary: string[]; sections: LegalSection[]};

const PLACEHOLDER = '[待律师审阅] 本节正文尚未定稿，以法律文本草稿为准。';

const sec = (id: string, title: string, body = PLACEHOLDER): LegalSection => ({id, title, body});

export const LEGAL_DOCS: LegalDoc[] = [
  {
    slug: 'privacy',
    title: '隐私政策',
    summary: [
      '不登录也能看；登录后我们保存观看记录、关注和设置，用来同步和推荐。',
      '分析和个性化广告需要你同意，随时可以在「Cookie 设置」里修改。',
      '浏览器发出的全局隐私控制（GPC）信号会被视为退出个性化广告。',
    ],
    sections: [
      sec('collect', '我们收集哪些信息', '[待律师审阅] 账号信息（邮箱、昵称）；观看记录；你主动提供的内容；设备与网络信息。具体范围以法律文本草稿为准。'),
      sec('use', '我们怎么使用'),
      sec('share', '与谁分享'),
      sec('rights', '你的权利', '[待律师审阅] 在「设置 › 隐私与数据」里可以暂停或清除观看记录。查看、导出、更正和删除数据的流程以法律文本草稿为准。'),
      sec('children', '儿童与青少年', '[待律师审阅] 本服务不面向 13 岁以下的儿童，注册时需要确认年龄。'),
      sec('retention', '保存多久'),
      sec('contact', '联系我们', '[待律师审阅] 联系方式待定。'),
    ],
  },
  {
    slug: 'terms',
    title: '用户协议',
    summary: ['使用本服务即表示同意本协议（定稿前以法律文本草稿为准）。'],
    sections: [sec('account', '账号规则'), sec('conduct', '使用规范'), sec('disclaimer', '免责声明'), sec('disputes', '争议解决')],
  },
  {
    slug: 'cookies',
    title: 'Cookie 政策',
    summary: ['必要 Cookie 始终开启；分析、个性化广告、个性化推荐需要你同意。', '你的选择保存在当前浏览器里，可以随时在页脚「Cookie 设置」修改。'],
    sections: [sec('categories', 'Cookie 类别'), sec('choices', '你的选择'), sec('gpc', '全局隐私控制（GPC）'), sec('third-parties', '第三方')],
  },
  {
    slug: 'copyright',
    title: '版权政策',
    summary: ['收到版权投诉后会暂时下架相关内容并核对。'],
    sections: [sec('notice', '投诉方式'), sec('agent', '指定代理人'), sec('counter', '反通知')],
  },
  {
    slug: 'community',
    title: '社区规则',
    summary: ['评论、弹幕、短评的发布规范。'],
    sections: [sec('rules', '基本规则'), sec('enforcement', '处理方式')],
  },
  {
    slug: 'ads',
    title: '广告说明',
    summary: ['个性化广告需要你同意；关闭后广告数量不变。'],
    sections: [sec('how', '广告怎么选'), sec('choices', '你的选择')],
  },
  {
    slug: 'performer-info',
    title: '演员信息更正与隐藏',
    summary: ['演员本人或经纪方可以申请更正或隐藏个人信息。'],
    sections: [sec('scope', '适用范围'), sec('request', '申请方式'), sec('effect', '隐藏后的效果')],
  },
];

export const legalDoc = (slug: string) => LEGAL_DOCS.find(d => d.slug === slug) ?? null;
