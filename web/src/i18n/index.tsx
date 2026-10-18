import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

export type Language = 'en' | 'zh';

const STORAGE_KEY = 'mengpo.language';

/**
 * Flat message catalogs. English is the source of truth: every key must exist
 * here, and `zh` mirrors it. Keeping them flat makes a missing translation an
 * obvious `en` fallback rather than a runtime blank.
 */
export const dictionaries = {
  en: {
    // Shell / navigation
    'nav.dashboard': 'Dashboard',
    'nav.sessions': 'Sessions',
    'nav.memories': 'Background Memory',
    'nav.candidates': 'Candidate Review',
    'nav.debugger': 'Projection Debugger',
    'nav.failures': 'Failure Analysis',
    'nav.evaluation': 'Evaluation',
    'nav.curation': 'Curation',
    'nav.members': 'Members',
    'nav.settings': 'Settings',
    'nav.primary': 'primary',
    'brand.tagline': 'Memory Console',
    'shell.signOut': 'Sign out',
    'shell.signedIn': 'signed in',
    'shell.member': 'member',
    'shell.activeTenant': 'Active tenant',
    'shell.live': 'live',
    'shell.consoleNotConfigured': 'Console API is not configured.',
    'language.switch': 'Switch language',
    'language.zh': '中文',
    'language.en': 'English',

    // Login
    'login.heroTitle': 'Memory that remembers, governably.',
    'login.heroBody': 'Review candidates, inspect projections and evaluate retrieval quality across every tenant.',
    'login.welcome': 'Welcome back',
    'login.required': 'Sign in required',
    'login.authenticate': 'Authenticate to open the memory console.',
    'signin.aria': 'sign in',
    'signin.registerAria': 'create account',
    'signin.email': 'Email',
    'signin.placeholder': 'you@example.com',
    'signin.displayName': 'Name (optional)',
    'signin.displayNamePlaceholder': 'How should we call you?',
    'signin.password': 'Password',
    'signin.passwordPlaceholder': 'At least 8 characters',
    'signin.signingIn': 'Signing in…',
    'signin.creating': 'Creating account…',
    'signin.signIn': 'Sign in',
    'signin.createAccount': 'Create account',
    'signin.needAccount': "Don't have an account? Create one",
    'signin.haveAccount': 'Already have an account? Sign in',

    // States
    'state.loading.title': 'Loading',
    'state.loading.desc': 'Fetching tenant-scoped data…',
    'state.empty.title': 'Nothing here yet',
    'state.empty.desc': 'No records match this view.',
    'state.error.title': 'Request failed',
    'state.error.desc': 'The memory service could not complete this request.',
    'state.permission.title': 'Permission required',
    'state.permission.desc': 'Your tenant role does not allow this action.',
    'state.conflict.title': 'Conflicting change',
    'state.conflict.desc': 'Someone else changed this record. Review and retry.',
    'state.stale.title': 'Stale data',
    'state.stale.desc': 'This view is out of date; refresh to continue.',
    'state.tenant-switch.title': 'Switching tenant',
    'state.tenant-switch.desc': 'Loading data for the selected tenant…',
    'state.rollback.title': 'Change reverted',
    'state.rollback.desc': 'The last optimistic change was rolled back.',
    'state.retry': 'Retry',

    // Pagination
    'pagination.aria': 'Pagination',
    'pagination.previous': 'Previous',
    'pagination.next': 'Next',
    'pagination.status': 'Page {page} of {totalPages} · {total} total',

    // Dashboard
    'dashboard.title': 'Dashboard',
    'dashboard.recentSessions': 'Recent sessions',
    'dashboard.recentMemories': 'Recent memories',
    'dashboard.evaluation': 'Evaluation',
    'dashboard.retrievalPrecision': 'Retrieval precision: {value}',
    'dashboard.cacheHitRate': 'Cache hit rate: {value}',
    'dashboard.empty': 'No sessions or memories for this tenant yet.',

    // Sessions
    'sessions.title': 'Session Explorer',
    'sessions.memories': 'Session memories',
    'sessions.empty': 'No sessions recorded for this tenant.',
    'sessions.none': 'No memories in this session.',
    'sessions.updated': 'Updated {time}',
    'sessions.titles': 'Titles',
    'sessions.sessions': 'Sessions',
    'sessions.sessionCount': '{count} sessions',
    'sessions.selectSession': 'Select a session to see its memories.',
    'sessions.closeDrawer': 'Close session memories',

    // Background memory
    'background.title': 'Background Memory',
    'background.empty': 'No background memories yet.',
    'background.filterAria': 'status filter',
    'background.filter.all': 'All',
    'background.filter.active': 'Active',
    'background.filter.stable': 'Stable',
    'background.filter.conflicted': 'Conflicted',
    'background.noneWithStatus': 'No memories with status {status}.',
    'background.stale': 'stale',
    'background.updated': 'Updated {time}',

    // Memory cards and detail
    'memory.confidence': 'confidence {value}',
    'memory.updated': 'Updated {time}',
    'memory.viewDetail': 'View detail',
    'memory.detail.title': 'Memory detail',
    'memory.detail.type': 'Type',
    'memory.detail.status': 'Status',
    'memory.detail.scope': 'Scope',
    'memory.detail.confidence': 'Confidence',
    'memory.detail.created': 'Created',
    'memory.detail.updated': 'Updated',
    'memory.detail.run': 'Source task',
    'memory.detail.viewRun': 'View source task',
    'memory.detail.content': 'Structured content',
    'memory.detail.contentText': 'Rendered text',
    'memory.detail.noContent': 'This memory has no structured content.',
    'memory.detail.evidence': 'Source events',
    'memory.detail.noEvidence': 'No source events are recorded for this memory.',
    'memory.detail.close': 'Close',
    'memory.scope.user-global': 'Background',
    'memory.scope.session': 'Session',

    // Candidate review
    'review.title': 'Candidate Review',
    'review.empty': 'No candidate memories awaiting review.',
    'review.conflict': 'This candidate changed while you were reviewing it.',
    'review.rollback': 'Could not {action} candidate {id}.',
    'review.action.confirm': 'confirm',
    'review.action.reject': 'reject',
    'review.action.correct': 'correct',
    'review.action.merge': 'merge',
    'review.action.expire': 'expire',
    'review.action.delete': 'delete',
    'review.proposedBy': 'proposed by',
    'review.evidence': 'evidence',
    'review.triage': 'triage',
    'review.duplicate': 'suspected duplicate',
  'review.duplicateTitle': 'Suspected duplicate group',
  'review.duplicateHint': 'These memories were grouped as near-duplicates. Compare them before merging.',
  'review.duplicateEmpty': 'This group has no readable members.',
  'review.duplicateClose': 'Close',
  'review.mergeTitle': 'Merge candidates',
  'review.mergeHint': 'Select the memories to merge into {id}. The target keeps its evidence; the others are retired and their evidence is combined.',
  'review.mergeEmpty': 'No other candidates are available to merge into this one.',
  'review.mergeSubmit': 'Merge selected',
  'review.mergeClose': 'Close',
    'review.conflicts': 'conflicts',

    // Projection debugger
    'debugger.title': 'Projection Debugger',
    'debugger.query': 'Query',
    'debugger.queryAria': 'query',
    'debugger.run': 'Run projection',
    'debugger.empty': 'No candidates were recalled for this query.',
    'debugger.mode': 'Mode',
    'debugger.reason': 'Reason',
    'debugger.cache': 'Cache',
    'debugger.hit': 'hit',
    'debugger.miss': 'miss',
    'debugger.budget': 'Budget',
    'debugger.usage': 'Usage',
    'debugger.degraded': 'Degraded',
    'debugger.memory': 'Memory',
    'debugger.included': 'Included',
    'debugger.score': 'Score',
    'debugger.reasonExclusion': 'Reason / Exclusion',
    'debugger.provenance': 'Provenance',
    'debugger.yes': 'yes',
    'debugger.no': 'no',
    'debugger.budgetValue': 'candidates={candidates} ranking={ranking} tokens={tokens}',
    'debugger.usageValue': 'seen={seen} ranked={ranked} injected={injected} tokens={tokens}',

    // Failure analysis
    'failures.title': 'Failure Analysis',
    'failures.empty': 'No failure memories recorded.',
    'failures.filterAria': 'confidence filter',
    'failures.attributionCompleteness': 'Attribution completeness',
    'failures.confidence.all': 'all',
    'failures.confidence.confirmed': 'confirmed',
    'failures.confidence.inferred': 'inferred',
    'failures.confidence.suspected': 'suspected',
    'failures.confidence.unknown': 'unknown',
    'failures.attribution.direct': 'direct',
    'failures.attribution.correlated': 'correlated',
    'failures.attribution.inferred': 'inferred',
    'failures.attribution.unknown': 'unknown',

    // Evaluation
    'evaluation.title': 'Memory Evaluation',
    'evaluation.generated': 'Generated {time}',
    'evaluation.retrieval_precision': 'Retrieval precision',
    'evaluation.promotion_precision': 'Promotion precision',
    'evaluation.wrong_memory_rate': 'Wrong memory rate',
    'evaluation.attribution_accuracy': 'Attribution accuracy',
    'evaluation.latency_ms_p95': 'Latency p95',
    'evaluation.tokens_per_projection': 'Tokens / projection',
    'evaluation.cost_usd': 'Cost',
    'evaluation.cache_hit_rate': 'Cache hit rate',
  'evaluation.noDataShort': 'no data yet',
  'evaluation.samples': 'projections {projections} · feedback samples {samples}',
  'evaluation.noData': 'No retrieval or feedback has been evaluated yet. Metrics appear once projections and feedback are recorded.',

    // Members
    'members.title': 'Members & Agents',
    'members.members': 'Members',
    'members.agents': 'Agents',
    'members.empty': 'No members or agents in this tenant.',
    'members.onlyAdmins': 'Only owners and tenant admins can manage members.',
    'members.onlyAdminsAgents': 'Only owners and tenant admins can manage agents.',
    'members.role': 'Role',
    'members.roleFor': 'role for {email}',
    'members.disable': 'disable',
    'members.agentName': 'Name',
    'members.agentScopes': 'Allowed scopes',
    'members.agentCapabilities': 'Capabilities',
    'members.createAgent': 'Create agent',
    'members.creatingAgent': 'Creating…',
    'members.agentSecretNotice': 'Copy this secret now — it is shown only once:',

    // Settings
    'settings.title': 'Settings',
    'settings.activeTenant': 'Active tenant',
    'settings.role': 'Role',
    'settings.none': 'none',
    'settings.defaultScope': 'default scope',
    'settings.provider.title': 'Memory LLM provider',
    'settings.provider.source': 'Source',
    'settings.provider.key': 'Key',
    'settings.provider.configured': 'configured',
    'settings.provider.notConfigured': 'not configured',
    'settings.provider.enabled': 'Enabled',
    'settings.provider.baseUrl': 'Base URL',
    'settings.provider.model': 'Model',
    'settings.provider.apiKey': 'API key',
    'settings.provider.leaveBlank': 'leave blank to keep',
    'settings.provider.save': 'Save',
    'settings.provider.test': 'Test connection',
    'settings.provider.saved': 'saved',
    'settings.provider.saveFailed': 'save failed',
    'settings.provider.testFailed': 'test failed',
    'settings.provider.testOk': 'ok ({latency} ms)',
    'settings.provider.testFailedPrefix': 'failed: {error}',
    'settings.provider.onlyAdmin': 'Only a tenant administrator can change the provider.',

    // Curation / analysis runs
    'curation.title': 'Curation',
    'curation.cadence': 'Cadence',
    'curation.nextRun': 'Next run',
    'curation.lastRun': 'Last run',
    'curation.lastResult': 'Last result',
    'curation.noSchedule': 'No curation schedule is configured.',
    'curation.empty': 'No curation runs yet.',
    'curation.task': 'Task',
    'curation.trigger': 'Trigger',
    'curation.model': 'Model',
    'curation.status': 'Status',
    'curation.candidates': 'Candidates',
    'curation.tokens': 'Tokens',
    'curation.latency': 'Latency',
    'curation.when': 'When',
  // Curation machine values: backend enums rendered as labels. They live under
  // their own namespace so a raw value falls back to itself, never to a key.
  'curation.task.classify_event': 'Classify event',
  'curation.task.analyze_failure': 'Analyze failure',
  'curation.task.consolidate_memory': 'Consolidate memory',
  'curation.task.analyze_conflict': 'Analyze conflict',
  'curation.task.propose_transfer': 'Propose transfer',
  'curation.task.dedupe_scan': 'Dedupe scan',
  'curation.task.retry_cleanup': 'Retry cleanup',
  'curation.task.evaluate_feedback': 'Evaluate feedback',
  'curation.task.candidate_promotion': 'Candidate promotion',
  'curation.task.candidate_triage': 'Candidate triage',
  'curation.trigger.event': 'Event',
  'curation.trigger.schedule': 'Schedule',
  'curation.trigger.explicit': 'Explicit',
  'curation.status.running': 'Running',
  'curation.status.succeeded': 'Succeeded',
  'curation.status.failed': 'Failed',
  'curation.status.dead_letter': 'Dead letter',
  'curation.status.cancelled': 'Cancelled',
  'curation.degraded.primary_timeout': 'primary timeout',
  'curation.degraded.rules_only': 'rules only',
  'curation.scheduleStatus.ok': 'ok',
  'curation.scheduleStatus.error': 'error',
  'curation.scheduleStatus.pending': 'pending',
  'curation.runNow': 'Run now',
  'curation.triggerTitle': 'Trigger a curation task',
  'curation.triggerHint': 'The worker runs the request on its next tick.',
  'curation.run': 'Run',
  'curation.runAll': 'Run all',
  'curation.requested': 'Requested',
  'curation.triggerEmpty': 'No curation tasks are available.',
  'curation.schedule.consolidate': 'Consolidate',
  'curation.schedule.expiry': 'Expiry',
  'curation.schedule.content-text-backfill': 'Content backfill',
  'curation.schedule.dedupe': 'Dedupe scan',
  'curation.schedule.conflict': 'Conflict scan',
  'curation.schedule.promotion': 'Candidate promotion',
  'curation.schedule.triage': 'Candidate triage',
  'curation.schedule.near-duplicate': 'Near-duplicate merge',
  'curation.schedule.cleanup': 'Cleanup',
  'curation.schedule.session-cleanup': 'Session cleanup',
  'curation.schedule.evaluation': 'Evaluation',
  'curation.schedule.cache-cleanup': 'Cache cleanup',
  'curation.schedule.outbox': 'Outbox dispatch',
  'curation.schedule.embedding': 'Embeddings',
  'curation.promptVersion': 'Prompt version',
  'curation.session': 'Session',
  'curation.confidence': 'confidence {value}',
  'curation.detail.title': 'Task detail',
  'curation.detail.close': 'Close',
  'curation.detail.result': 'Structured result',
  'curation.detail.noResult': 'This task recorded no structured result.',
  'curation.detail.inputs': 'Source events',
  'curation.detail.noInputs': 'No source events were recorded for this task.',
  'curation.detail.outputs': 'Produced memories',
  'curation.detail.noOutputs': 'No memories were linked to this task.',
  'curation.detail.truncated': 'Only the first 50 items of each side are shown.',

    // Error boundary
    'error.title': 'Something went wrong',
    'error.retry': 'Retry',
  },
  zh: {
    'nav.dashboard': '仪表盘',
    'nav.sessions': '会话',
    'nav.memories': '长期记忆',
    'nav.candidates': '候选审核',
    'nav.debugger': '投影调试',
    'nav.failures': '失败分析',
    'nav.evaluation': '评估',
    'nav.curation': '记忆整理',
    'nav.members': '成员',
    'nav.settings': '设置',
    'nav.primary': '主导航',
    'brand.tagline': '记忆控制台',
    'shell.signOut': '退出登录',
    'shell.signedIn': '已登录',
    'shell.member': '成员',
    'shell.activeTenant': '当前租户',
    'shell.live': '实时',
    'shell.consoleNotConfigured': '控制台 API 未配置。',
    'language.switch': '切换语言',
    'language.zh': '中文',
    'language.en': 'English',

    'login.heroTitle': '可治理的记忆，真正记得住。',
    'login.heroBody': '审核候选、检查投影，并评估每个租户的召回质量。',
    'login.welcome': '欢迎回来',
    'login.required': '需要登录',
    'login.authenticate': '请先登录以打开记忆控制台。',
    'signin.aria': '登录',
    'signin.registerAria': '注册',
    'signin.email': '邮箱',
    'signin.placeholder': 'you@example.com',
    'signin.displayName': '名称（选填）',
    'signin.displayNamePlaceholder': '怎么称呼你？',
    'signin.password': '密码',
    'signin.passwordPlaceholder': '至少 8 个字符',
    'signin.signingIn': '登录中…',
    'signin.creating': '注册中…',
    'signin.signIn': '登录',
    'signin.createAccount': '注册账号',
    'signin.needAccount': '还没有账号？注册一个',
    'signin.haveAccount': '已有账号？去登录',

    'state.loading.title': '加载中',
    'state.loading.desc': '正在获取租户数据…',
    'state.empty.title': '暂无内容',
    'state.empty.desc': '没有符合当前视图的记录。',
    'state.error.title': '请求失败',
    'state.error.desc': '记忆服务无法完成该请求。',
    'state.permission.title': '需要权限',
    'state.permission.desc': '你的租户角色无权执行该操作。',
    'state.conflict.title': '变更冲突',
    'state.conflict.desc': '该记录已被他人修改，请检查后重试。',
    'state.stale.title': '数据已过期',
    'state.stale.desc': '该视图已过期，请刷新后继续。',
    'state.tenant-switch.title': '正在切换租户',
    'state.tenant-switch.desc': '正在为所选租户加载数据…',
    'state.rollback.title': '变更已回滚',
    'state.rollback.desc': '上一次乐观更新已回滚。',
    'state.retry': '重试',

    'pagination.aria': '分页',
    'pagination.previous': '上一页',
    'pagination.next': '下一页',
    'pagination.status': '第 {page} / {totalPages} 页 · 共 {total} 条',

    'dashboard.title': '仪表盘',
    'dashboard.recentSessions': '最近会话',
    'dashboard.recentMemories': '最近记忆',
    'dashboard.evaluation': '评估',
    'dashboard.retrievalPrecision': '召回准确率：{value}',
    'dashboard.cacheHitRate': '缓存命中率：{value}',
    'dashboard.empty': '该租户暂无会话或记忆。',

    'sessions.title': '会话浏览器',
    'sessions.memories': '会话记忆',
    'sessions.empty': '该租户暂无会话记录。',
    'sessions.none': '该会话暂无记忆。',
    'sessions.updated': '更新于 {time}',
    'sessions.titles': '标题',
    'sessions.sessions': '会话',
    'sessions.sessionCount': '{count} 个会话',
    'sessions.selectSession': '选择会话以查看记忆。',
    'sessions.closeDrawer': '关闭会话记忆',

    'background.title': '长期记忆',
    'background.empty': '暂无长期记忆。',
    'background.filterAria': '状态筛选',
    'background.filter.all': '全部',
    'background.filter.active': '生效中',
    'background.filter.stable': '稳定',
    'background.filter.conflicted': '冲突',
    'background.noneWithStatus': '没有状态为 {status} 的记忆。',
    'background.stale': '已过期',
    'background.updated': '更新于 {time}',

    // 记忆卡片与详情
    'memory.confidence': '置信度 {value}',
    'memory.updated': '更新于 {time}',
    'memory.viewDetail': '查看详情',
    'memory.detail.title': '记忆详情',
    'memory.detail.type': '类型',
    'memory.detail.status': '状态',
    'memory.detail.scope': '范围',
    'memory.detail.confidence': '置信度',
    'memory.detail.created': '创建时间',
    'memory.detail.updated': '更新时间',
    'memory.detail.run': '来源任务',
    'memory.detail.viewRun': '查看来源任务',
    'memory.detail.content': '结构化内容',
    'memory.detail.contentText': '渲染文本',
    'memory.detail.noContent': '这条记忆没有结构化内容。',
    'memory.detail.evidence': '原始事件',
    'memory.detail.noEvidence': '这条记忆没有记录原始事件。',
    'memory.detail.close': '关闭',
    'memory.scope.user-global': '长期',
    'memory.scope.session': '会话',

    'review.title': '候选审核',
    'review.empty': '暂无待审核的候选记忆。',
    'review.conflict': '在你审核期间该候选已发生变化。',
    'review.rollback': '无法对候选 {id} 执行 {action}。',
    'review.action.confirm': '确认',
    'review.action.reject': '拒绝',
    'review.action.correct': '修正',
    'review.action.merge': '合并',
    'review.action.expire': '过期',
    'review.action.delete': '删除',
    'review.proposedBy': '提出者',
    'review.evidence': '证据',
    'review.triage': '分诊',
    'review.duplicate': '疑似重复',
  'review.duplicateTitle': '疑似重复组',
  'review.duplicateHint': '这些记忆被归为近似重复，合并前请先核对内容。',
  'review.duplicateEmpty': '该组没有可读取的成员。',
  'review.duplicateClose': '关闭',
  'review.mergeTitle': '合并候选',
  'review.mergeHint': '选择要合并到 {id} 的记忆。目标保留其证据，其余被 retire 并把证据并进来。',
  'review.mergeEmpty': '没有其他候选可以合并进来。',
  'review.mergeSubmit': '合并选中项',
  'review.mergeClose': '关闭',
    'review.conflicts': '冲突',

    'debugger.title': '投影调试',
    'debugger.query': '查询',
    'debugger.queryAria': '查询',
    'debugger.run': '运行投影',
    'debugger.empty': '该查询未召回任何候选。',
    'debugger.mode': '模式',
    'debugger.reason': '原因',
    'debugger.cache': '缓存',
    'debugger.hit': '命中',
    'debugger.miss': '未命中',
    'debugger.budget': '预算',
    'debugger.usage': '用量',
    'debugger.degraded': '降级',
    'debugger.memory': '记忆',
    'debugger.included': '已包含',
    'debugger.score': '分数',
    'debugger.reasonExclusion': '原因 / 排除',
    'debugger.provenance': '来源',
    'debugger.yes': '是',
    'debugger.no': '否',
    'debugger.budgetValue': '候选={candidates} 排序={ranking} tokens={tokens}',
    'debugger.usageValue': '已见={seen} 已排序={ranked} 已注入={injected} tokens={tokens}',

    'failures.title': '失败分析',
    'failures.empty': '暂无失败记忆。',
    'failures.filterAria': '置信度筛选',
    'failures.attributionCompleteness': '归因完整性',
    'failures.confidence.all': '全部',
    'failures.confidence.confirmed': '已确认',
    'failures.confidence.inferred': '推断',
    'failures.confidence.suspected': '疑似',
    'failures.confidence.unknown': '未知',
    'failures.attribution.direct': '直接',
    'failures.attribution.correlated': '相关',
    'failures.attribution.inferred': '推断',
    'failures.attribution.unknown': '未知',

    'evaluation.title': '记忆评估',
    'evaluation.generated': '生成于 {time}',
    'evaluation.retrieval_precision': '召回准确率',
    'evaluation.promotion_precision': '晋升准确率',
    'evaluation.wrong_memory_rate': '错误记忆率',
    'evaluation.attribution_accuracy': '归因准确率',
    'evaluation.latency_ms_p95': 'P95 延迟',
    'evaluation.tokens_per_projection': '每次投影 tokens',
    'evaluation.cost_usd': '成本',
    'evaluation.cache_hit_rate': '缓存命中率',
  'evaluation.noDataShort': '暂无数据',
  'evaluation.samples': '投影 {projections} 次 · 反馈样本 {samples} 条',
  'evaluation.noData': '暂无可评估数据。产生投影与反馈后，指标会自动出现。',

    'members.title': '成员与智能体',
    'members.members': '成员',
    'members.agents': '智能体',
    'members.empty': '该租户暂无成员或智能体。',
    'members.onlyAdmins': '仅所有者与租户管理员可管理成员。',
    'members.onlyAdminsAgents': '仅所有者与租户管理员可管理智能体。',
    'members.role': '角色',
    'members.roleFor': '{email} 的角色',
    'members.disable': '停用',
    'members.agentName': '名称',
    'members.agentScopes': '允许范围',
    'members.agentCapabilities': '能力',
    'members.createAgent': '创建智能体',
    'members.creatingAgent': '创建中…',
    'members.agentSecretNotice': '请立即复制此密钥——仅显示一次：',

    'settings.title': '设置',
    'settings.activeTenant': '当前租户',
    'settings.role': '角色',
    'settings.none': '无',
    'settings.defaultScope': '默认作用域',
    'settings.provider.title': '记忆 LLM 提供方',
    'settings.provider.source': '来源',
    'settings.provider.key': '密钥',
    'settings.provider.configured': '已配置',
    'settings.provider.notConfigured': '未配置',
    'settings.provider.enabled': '已启用',
    'settings.provider.baseUrl': '基础 URL',
    'settings.provider.model': '模型',
    'settings.provider.apiKey': 'API 密钥',
    'settings.provider.leaveBlank': '留空表示不修改',
    'settings.provider.save': '保存',
    'settings.provider.test': '测试连接',
    'settings.provider.saved': '已保存',
    'settings.provider.saveFailed': '保存失败',
    'settings.provider.testFailed': '测试失败',
    'settings.provider.testOk': '正常（{latency} ms）',
    'settings.provider.testFailedPrefix': '失败：{error}',
    'settings.provider.onlyAdmin': '仅租户管理员可以修改提供方。',

    'curation.title': '记忆整理',
    'curation.cadence': '周期',
    'curation.nextRun': '下次运行',
    'curation.lastRun': '上次运行',
    'curation.lastResult': '上次结果',
    'curation.noSchedule': '未配置整理计划。',
    'curation.empty': '暂无整理运行记录。',
    'curation.task': '任务',
    'curation.trigger': '触发',
    'curation.model': '模型',
    'curation.status': '状态',
    'curation.candidates': '候选',
    'curation.tokens': 'Tokens',
    'curation.latency': '延迟',
    'curation.when': '时间',
  'curation.task.classify_event': '事件分类',
  'curation.task.analyze_failure': '失败分析',
  'curation.task.consolidate_memory': '记忆固化',
  'curation.task.analyze_conflict': '冲突分析',
  'curation.task.propose_transfer': '迁移建议',
  'curation.task.dedupe_scan': '去重扫描',
  'curation.task.retry_cleanup': '重试清理',
  'curation.task.evaluate_feedback': '反馈评估',
  'curation.task.candidate_promotion': '候选晋升',
  'curation.task.candidate_triage': '候选分诊',
  'curation.trigger.event': '事件',
  'curation.trigger.schedule': '计划',
  'curation.trigger.explicit': '显式',
  'curation.status.running': '运行中',
  'curation.status.succeeded': '成功',
  'curation.status.failed': '失败',
  'curation.status.dead_letter': '死信',
  'curation.status.cancelled': '已取消',
  'curation.degraded.primary_timeout': '主模型超时',
  'curation.degraded.rules_only': '仅规则',
  'curation.scheduleStatus.ok': '正常',
  'curation.scheduleStatus.error': '错误',
  'curation.scheduleStatus.pending': '待运行',
  'curation.runNow': '立即运行',
  'curation.triggerTitle': '触发整理任务',
  'curation.triggerHint': 'worker 会在下个调度周期执行该请求。',
  'curation.run': '运行',
  'curation.runAll': '全部运行',
  'curation.requested': '已请求',
  'curation.triggerEmpty': '没有可触发的整理任务。',
  'curation.schedule.consolidate': '记忆固化',
  'curation.schedule.expiry': '过期清理',
  'curation.schedule.content-text-backfill': '正文回填',
  'curation.schedule.dedupe': '去重扫描',
  'curation.schedule.conflict': '冲突扫描',
  'curation.schedule.promotion': '候选晋升',
  'curation.schedule.triage': '候选分诊',
  'curation.schedule.near-duplicate': '近似去重',
  'curation.schedule.cleanup': '清理',
  'curation.schedule.session-cleanup': '会话清理',
  'curation.schedule.evaluation': '评估',
  'curation.schedule.cache-cleanup': '缓存清理',
  'curation.schedule.outbox': '作业派发',
  'curation.schedule.embedding': '向量生成',
  'curation.promptVersion': '提示词版本',
  'curation.session': '会话',
  'curation.confidence': '置信度 {value}',
  'curation.detail.title': '任务详情',
  'curation.detail.close': '关闭',
  'curation.detail.result': '结构化结果',
  'curation.detail.noResult': '该任务没有记录结构化结果。',
  'curation.detail.inputs': '数据源事件',
  'curation.detail.noInputs': '该任务没有记录数据源事件。',
  'curation.detail.outputs': '产出记忆',
  'curation.detail.noOutputs': '该任务没有关联产出记忆。',
  'curation.detail.truncated': '每侧仅展示前 50 条。',

    'error.title': '出错了',
    'error.retry': '重试',
  },
} as const;

export type MessageKey = keyof typeof dictionaries.en;

export function interpolate(template: string, vars?: Record<string, string | number>): string {
  if (!vars) {
    return template;
  }
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match,
  );
}

export interface I18nValue {
  language: Language;
  setLanguage: (language: Language) => void;
  toggle: () => void;
  t: (key: string, vars?: Record<string, string | number>) => string;
  tEnum: (prefix: string, value: string) => string;
}

function lookup(language: Language, key: string): string {
  const catalog = dictionaries[language] as Record<string, string>;
  return catalog[key] ?? (dictionaries.en as Record<string, string>)[key] ?? key;
}

// enumLookup translates a machine value (a backend enum such as a task or run
// status) through the catalog. Unlike `t`, an unknown value falls back to the
// raw wire value rather than the namespaced key, so operators still see
// something meaningful when a new enum arrives before its translation.
function enumLookup(language: Language, prefix: string, value: string): string {
  if (!value) {
    return '';
  }
  const key = `${prefix}.${value}`;
  const catalog = dictionaries[language] as Record<string, string>;
  return catalog[key] ?? (dictionaries.en as Record<string, string>)[key] ?? value;
}

function detectLanguage(): Language {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    if (stored === 'en' || stored === 'zh') {
      return stored;
    }
  } catch {
    /* storage may be unavailable (private mode); fall through to navigator */
  }
  const navigatorLanguage = typeof navigator !== 'undefined' ? navigator.language ?? '' : '';
  return navigatorLanguage.toLowerCase().startsWith('zh') ? 'zh' : 'en';
}

// The fallback keeps components usable without a provider (tests, isolated
// renders) and pins them to English, which is the source catalog.
const fallback: I18nValue = {
  language: 'en',
  setLanguage: () => {},
  toggle: () => {},
  t: (key, vars) => interpolate(lookup('en', key), vars),
  tEnum: (prefix, value) => enumLookup('en', prefix, value),
};

const I18nContext = createContext<I18nValue>(fallback);

export interface LanguageProviderProps {
  children: ReactNode;
  initialLanguage?: Language;
}

export function LanguageProvider({ children, initialLanguage }: LanguageProviderProps) {
  const [language, setLanguageState] = useState<Language>(() => initialLanguage ?? detectLanguage());

  useEffect(() => {
    document.documentElement.lang = language === 'zh' ? 'zh-CN' : 'en';
    try {
      window.localStorage.setItem(STORAGE_KEY, language);
    } catch {
      /* persisting the preference is best-effort */
    }
  }, [language]);

  const setLanguage = useCallback((next: Language) => setLanguageState(next), []);
  const toggle = useCallback(() => setLanguageState((current) => (current === 'en' ? 'zh' : 'en')), []);

  const value = useMemo<I18nValue>(
    () => ({
      language,
      setLanguage,
      toggle,
      t: (key, vars) => interpolate(lookup(language, key), vars),
      tEnum: (prefix, value) => enumLookup(language, prefix, value),
    }),
    [language, setLanguage, toggle],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  return useContext(I18nContext);
}
