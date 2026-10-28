(() => {
  'use strict';
  const $ = id => document.getElementById(id);
  const names = {pass: '通过', fail: '异常', unknown: '未知', not_applicable: '不适用'};
  let session = '', report = null, plan = null, busy = false, ended = false;
  let repairPlan = null, operation = null, pollTimer = null, cleanupOnly = false;
  const token = location.hash.slice(1);
  history.replaceState(null, '', '/');
  const text = (tag, value, className = '') => { const element = document.createElement(tag); element.textContent = value; element.className = className; return element; };
  function controls() {
    $('scan').disabled = busy || !session || ended;
    $('plan').disabled = busy || !report || ended;
    $('export').disabled = busy || !report;
    $('close').disabled = busy || !session || ended;
    $('confirm-network').disabled = busy || !plan?.allowed || plan?.cancelRequested || ended;
  const active = operation && !operation.finished;
  const retained = !!operation?.artifact_retained;
  $('repair-cleanup').hidden = !retained;
  $('repair-cleanup').disabled = busy || !operation?.cleanup_retry_allowed || ended;
  $('repair-preview').disabled = busy || !report?.repair_available || active || retained || cleanupOnly || ended;
  $('repair-confirm').disabled = busy || !repairPlan?.allowed || repairPlan?.cancelRequested || ended;
  $('repair-stop').disabled = busy || !active || ended || operation?.process?.program_stop_allowed === false;
    for (const id of ['network-query','network-end','repair-query','repair-end']) $(id).disabled = busy || !session || ended;
  $('scan').disabled ||= !!active || retained || cleanupOnly;
  $('plan').disabled ||= !!active || retained || cleanupOnly;
  $('close').textContent = active ? '停止本次 Claude 并结束服务' : '结束本机服务';
  }
  async function api(path, body = {}, bootstrap = '') {
    const headers = {'Content-Type': 'application/json'};
    if (bootstrap) headers['X-Bootstrap'] = bootstrap; else headers['X-Session'] = session;
    const response = await fetch(path, {method: 'POST', headers, body: JSON.stringify(body), credentials: 'omit', cache: 'no-store', redirect: 'error'});
    if (!response.ok) {
      const error = new Error(response.status === 409 ? '清单已过期或已使用，请重新检查并预览。' : '本机请求未完成。服务可能已关闭，或本次会话无效。');
    error.replyStatus = response.status;
      if (['/api/cancel','/api/plan-status','/api/repair-cancel','/api/repair-plan-status'].includes(path)) { try { error.planState = (await response.json()).state; } catch (_) {} }
      throw error;
    }
    return response.json();
  }
  async function action(work) {
    if (busy || ended) return;
    busy = true; controls();
    try { await work(); } catch (error) { $('status').textContent = error.message === 'Failed to fetch' ? '无法连接本机服务，请重新启动并使用新的链接。' : error.message; }
    finally { busy = false; controls(); }
  }
  function render(data) {
    report = data;
    $('overview-heading').textContent = data.score.scope;
    $('score-unverified').textContent = `未验证：${data.score.unverified.join('；')}。即使得分 100，也不表示整机环境或 Claude 可用性通过。`;
    $('score').textContent = data.score.value === null ? '—' : data.score.value;
    $('score').className = 'score-number' + (data.score.value === null ? '' : data.score.value < 80 ? ' warn' : ' good');
    $('score-reason').textContent = data.score.value === null ? '未评分：覆盖不足或关键证据未知' : data.score.reasons.includes('critical_failure_cap_49') ? '存在关键失败，已限制最高分' : '仅反映本次限定证据';
    $('coverage').textContent = `证据覆盖率 ${Math.round(data.score.coverage * 100)}% · 已判断权重 ${data.score.judged_weight}/${data.score.applicable_weight}`;
    $('summary').textContent = data.mode === 'probe' ? ({completed:'单次参考探测已完成',failed:'参考检查未通过',blocked:'参考检查已中止',cancelled:'参考检查已取消',expired:'参考授权过期，未执行',not_started:'参考检查未执行'})[data.probe?.state] || '参考执行状态未确认' : '本地只读检查已结束';
    $('scope').textContent = data.scope;
    for (const status of ['pass', 'fail', 'unknown']) $(status).textContent = data.checks.filter(c => c.status === status).length;
    $('sample-time').textContent = `本次采样 ${new Date(data.observed_at).toLocaleString()} · ${data.mode === 'probe' ? '无账户/模型验证' : '未主动外联'}`;
    $('policy').textContent = `策略 ${data.score.policy}`;
    $('checks').replaceChildren();
    for (const check of data.checks) {
      const item = text('details', '', 'check');
      const heading = text('summary', '');
      heading.append(text('span', check.title, 'check-title'), text('span', names[check.status] || '未知', `state ${check.status}`));
      const body = text('div', '', 'detail');
      body.append(text('p', check.detail), text('p', `证据 ${check.source} · ${new Date(check.observed_at).toLocaleString()}`), text('code', check.code));
      if (check.steps?.length) {
        const list = text('ul', '');
        for (const step of check.steps) list.append(text('li', `${names[step.status] || '未知'} · ${step.detail} (${step.code})`));
        body.append(list);
      }
      item.append(heading, body); $('checks').append(item);
    }
    $('limitations').replaceChildren(...data.limitations.map(value => text('li', value)));
    $('limits').hidden = false;
    $('status').textContent = '本次限定报告已更新；分数不代表实际 Claude 路由、账户或模型通过。';
  }
  $('scan').addEventListener('click', () => action(async () => {
    plan = null; $('status').textContent = '正在进行本地只读检查；没有主动外联…';
    render(await api('/api/scan'));
  }));
  $('plan').addEventListener('click', () => action(async () => {
    plan = await api('/api/plan');
    $('plan-reason').textContent = plan.allowed ? `固定目标 ${plan.target} · 总超时 ${plan.timeout_seconds} 秒 · 清单 60 秒内有效` : plan.reason;
    $('manifest').replaceChildren();
    if (plan.allowed) for (const [title, values] of [['接收方', plan.recipients], ['最多执行的操作', plan.operations], ['发送的数据', plan.data], ['适用范围', plan.notes]]) {
      const list = text('ul', ''); list.append(...values.map(value => text('li', value)));
      $('manifest').append(text('h3', title), list);
    }
    $('network-dialog').showModal();
  }));
  function cancelPlan() {
    if (!plan || busy) return;
    plan.cancelRequested = true;
    action(async () => {
      try {
        const result = await api('/api/cancel', {plan_id: plan.id});
        $('status').textContent = result.state === 'cancelled' ? '服务端已撤销清单；旧清单不能再启动探测。' : '清单已过期，不能再启动探测。';
      } catch (error) {
        if (['running', 'completed'].includes(error.planState)) $('status').textContent = '清单已进入执行，无法撤销已发生的请求；不会重放。';
        else if (error.planState === 'not_current') $('status').textContent = '该清单已不属于当前基线，不能重新执行；这不代表撤销了之前的操作。';
        else { error.message = '撤销尚未获服务端确认；确认执行已禁用。可再次取消、查询清单状态，或使用此弹窗内的结束服务按钮。'; $('plan-reason').textContent = error.message; throw error; }
      }
      plan = null; $('network-dialog').close(); $('plan').focus();
    });
  }
  $('cancel-network').addEventListener('click', cancelPlan);
  $('network-dialog').addEventListener('cancel', event => { event.preventDefault(); cancelPlan(); });
  $('confirm-network').addEventListener('click', () => action(async () => {
    const id = plan.id; plan = null; $('network-dialog').close();
    $('status').textContent = '正在执行已确认的单次参考探测，失败不重试…';
    render(await api('/api/probe', {plan_id: id}));
    $('plan').focus();
  }));
  $('export').addEventListener('click', () => {
    if (!report) return;
    const exported = operation?.process ? {reference_report:report,execution:operation.process,baseline:operation.baseline || null,comparable:operation.comparable,limitations:operation.limitations} : report;
    const url = URL.createObjectURL(new Blob([JSON.stringify(exported, null, 2)], {type: 'application/json'}));
    const link = document.createElement('a'); link.href = url; link.download = 'cc-health-report.json'; link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  $('close').addEventListener('click', () => action(async () => {
    const result=await api('/api/close'); if (result.cleanup_only) { cleanupOnly=true; operation={id:result.operation_id,finished:false}; await pollOperation(); return; } ended = true; session = ''; plan = null;
  clearTimeout(pollTimer);
  $('status').textContent = operation && !operation.finished ? '本机服务已结束，并已请求停止本次 Claude 根进程。此页不能确认最终退出，请检查原终端；未声称撤销副作用或清空后代。' : '本机服务已结束；当前页面仍可查看或导出已取得的脱敏结果。';
  }));
  function renderOperation(data) {
    operation = data; cleanupOnly ||= !!data.cleanup_only;
    const labels = {accepted:'已接受，正在重新核对绑定证据',starting:'正在启动',running:'本次根进程运行中',exited:'本次根进程已退出',stopped:'本次根进程已停止',stop_requested:'已请求停止，尚未确认退出',cancelled_before_start:'在启动前撤销，未启动进程',invalidated:'启动证据、可执行文件或终端条件变化，未启动进程',start_failed:'启动失败',exit_unconfirmed:'进程退出未确认，已禁用程序信号'};
    $('repair-state').textContent = labels[data.process?.state || data.state] || data.state;
    $('repair-evidence').textContent = `${data.process?.environment_delivered ? '指定环境已交付新进程。' : '尚无环境交付证据。'} 参考复测：${({not_started:'未执行',running:'执行中',completed:'已完成',cancelled:'已取消',expired:'授权过期，未执行',failed:'执行失败',blocked:'证据不足，已停止'})[data.reference_state] || '未知'}。${data.comparable ? '基线与复测采用相同参考策略，路线是本次变更项。' : '缺少新鲜的可比参考基线，不作因果改善结论。'} ${data.process?.exit_code !== undefined ? `根进程退出码 ${data.process.exit_code}；终端恢复${data.process.terminal_restored ? '已确认' : '未确认'}。` : ''}`;
    if (data.reference) render(data.reference);
    $('repair-limitations').replaceChildren(...data.limitations.map(value => text('li', value)));
    if (data.process?.stop_reason) $('repair-limitations').append(text('li','当前无法安全确认程序停止能力；不会向不明进程发信号。'));
    if (['cleanup_identity_changed','cleanup_identity_unconfirmed','cleanup_contents_changed','cleanup_restore_pending'].includes(data.cleanup_code)) $('repair-limitations').append(text('li',data.cleanup_code === 'cleanup_restore_pending' ? '检测到对象置换，隔离对象还原尚未完成；已停止删除并保留恢复对象，请保持本会话。' : '临时对象身份或内容已变化；已停止删除并保留恢复对象，请恢复原布局后在本会话重试。'));
    if (data.artifact_retained) $('repair-limitations').append(text('li','本次临时入口副本清理尚未完成，已阻止新启动。请点击重试清理；成功前保留恢复对象。'));
    if (cleanupOnly) $('repair-limitations').append(text('li','服务活动期限已结束或已请求结束；现在仅保留状态和本次清理重试，没有新的检查、启动或参考网络请求。'));
    controls();
  }
  async function pollOperation() {
    if (!operation || ended) return;
    try { renderOperation(await api('/api/repair-status', {plan_id:operation.id})); }
    catch (_) { $('repair-state').textContent = '无法取得运行状态；不要重复确认启动。请查看原终端。'; return; }
    if (!operation.finished) pollTimer = setTimeout(pollOperation, 700);
  }
  $('repair-preview').addEventListener('click', () => action(async () => {
    repairPlan = await api('/api/repair-plan', {loopback:$('proxy-loopback').value,port:Number($('proxy-port').value)});
    $('repair-reason').textContent = repairPlan.allowed ? `将执行已批准入口的私有 Mach-O 副本，原生兼容性有范围限制；来源 ${repairPlan.executable}；摘要 ${repairPlan.approved_digest}；工作目录 ${repairPlan.directory}；代理 ${repairPlan.proxy}。完整路径仅在这张本地确认卡显示，供你核对，不写入日志或导出。不能完整防御同权限恶意篡改。计划 60 秒有效。` : repairPlan.reason;
    $('repair-manifest').replaceChildren();
    for (const [title,values] of [['环境变更',repairPlan.changes],['Claude 正常启动的独立影响与限制',repairPlan.warnings],['仅参考复测的接收方',repairPlan.reference.recipients || []],['仅参考复测的最多操作',repairPlan.reference.operations || []],['仅参考复测发送的数据',repairPlan.reference.data || []]]) {
      const list=text('ul','');list.append(...values.map(value=>text('li',value)));$('repair-manifest').append(text('h3',title),list);
    }
    $('repair-dialog').showModal();
  }));
  function cancelRepair() {
    if (!repairPlan || busy) return;
    repairPlan.cancelRequested=true;
    action(async () => {
      try { const result=await api('/api/repair-cancel',{plan_id:repairPlan.id});$('status').textContent=result.cancelled ? '服务端已撤销新会话计划；不会据此启动。' : '新会话计划已过期。'; }
      catch (_) { $('repair-reason').textContent='撤销未获服务端确认；执行已禁用。可重试取消、查询新会话状态，或使用此弹窗内的结束服务按钮；不能推断已开始操作被撤销。';return; }
      repairPlan=null;$('repair-dialog').close();
    });
  }
  $('repair-cancel').addEventListener('click',cancelRepair);
  $('repair-dialog').addEventListener('cancel',event=>{event.preventDefault();cancelRepair();});
  $('repair-confirm').addEventListener('click',()=>action(async()=>{
    const id=repairPlan.id;repairPlan=null;$('repair-dialog').close();
    // A lost response is an unknown outcome, never permission to replay.
    operation={id,finished:false};controls();
    try { renderOperation(await api('/api/repair-start',{plan_id:id}));pollTimer=setTimeout(pollOperation,100); }
    catch (error) { if (error.replyStatus === 409) { operation.finished=true;$('repair-state').textContent='服务端拒绝本次启动；请重新检查并生成新计划。'; } else { $('repair-state').textContent='启动确认结果未知；已禁止再次启动，请查询状态或查看原终端。';pollTimer=setTimeout(pollOperation,100); } }
  }));
  $('repair-cleanup').addEventListener('click',()=>action(async()=>{renderOperation(await api('/api/repair-cleanup',{plan_id:operation.id}));$('status').textContent=operation.artifact_retained ? '清理仍未完成，恢复对象已保留，可以再次重试。' : cleanupOnly ? '本次临时副本已清理，本机服务将结束。' : '本次临时副本已清理，可以重新检查并预览新计划。';}));
  $('repair-stop').addEventListener('click',()=>action(async()=>{renderOperation(await api('/api/repair-stop',{plan_id:operation.id}));clearTimeout(pollTimer);pollTimer=setTimeout(pollOperation,100);}));
  function endFromDialog() {
    action(async () => {
      try { const result=await api('/api/close'); if(result.cleanup_only) {cleanupOnly=true;operation={id:result.operation_id,finished:false};$('network-dialog').close();$('repair-dialog').close();await pollOperation();return;} }
      catch (error) { if (error.replyStatus !== 410) { const message='结束请求尚未获确认；可重试本按钮或查询状态，不要重新确认启动。服务最多运行 15 分钟；终端 Ctrl-C 是前台进程组行为，并非关闭服务的保证。';$('plan-reason').textContent=message;$('repair-reason').textContent=message;return; } }
      ended=true;session='';clearTimeout(pollTimer);plan=null;repairPlan=null;
      $('network-dialog').close();$('repair-dialog').close();
      $('status').textContent='本机服务已请求结束；已开始的根进程是否退出请以终端回执为准，不代表副作用或所有后代已回滚。';
    });
  }
  function queryPlan(isRepair) {
    const pending=isRepair ? repairPlan : plan;if (!pending) return;
    action(async () => {
      let state;
      try { state=(await api(isRepair ? '/api/repair-plan-status' : '/api/plan-status',{plan_id:pending.id})).state; }
      catch (error) { if (error.planState==='not_current') state='not_current';else { $(isRepair ? 'repair-reason' : 'plan-reason').textContent='暂时无法查询状态；批准仍禁用，可重试查询或在此弹窗内结束服务。';return; } }
      if (state==='ready') { $(isRepair ? 'repair-reason' : 'plan-reason').textContent='服务端仍为待执行；取消尚未完成。请重试取消，确认执行保持禁用。';pending.cancelRequested=true;return; }
      $(isRepair ? 'repair-dialog' : 'network-dialog').close();
      if (isRepair) repairPlan=null;else plan=null;
      if (state==='cancelled') $('status').textContent='已查询确认：服务端清单已撤销，旧计划不能执行。';
      else if (state==='expired') $('status').textContent='已查询确认：清单过期，未恢复批准。';
      else { $('status').textContent='清单已开始、已使用或不再属于当前基线；不能推断此前操作已撤销。';if (isRepair && state==='consumed') {operation={id:pending.id,finished:false};pollTimer=setTimeout(pollOperation,100);} }
    });
  }
  $('network-query').addEventListener('click',()=>queryPlan(false));
  $('repair-query').addEventListener('click',()=>queryPlan(true));
  $('network-end').addEventListener('click',endFromDialog);
  $('repair-end').addEventListener('click',endFromDialog);
  if (!/^[a-f0-9]{64}$/.test(token)) { $('status').textContent = '缺少一次性链接。请从终端重新启动 cc-health serve 并打开它提供的完整链接。'; return; }
  api('/api/session', {}, token).then(result => { session = result.session; $('status').textContent = '本机会话已就绪；尚未检查。请先阅读本地检查范围。'; controls(); }).catch(() => { $('status').textContent = '一次性链接已使用或服务已结束。请重新启动以建立新会话。'; });
})();
