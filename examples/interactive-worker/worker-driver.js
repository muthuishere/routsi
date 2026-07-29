// devin-live worker driver v2: tools-aware. Poll routsi -> job file (messages+tools)
// -> interactive devin (ghostty) -> JSON answer file -> content OR tool_calls back.
const { execFileSync } = require('child_process');
const fs = require('fs'), path = require('path'), os = require('os');
const PROXY = 'http://127.0.0.1:18080';
const SK = process.argv[2], SPOOL = process.argv[3];
const QUEUE = process.argv[4] || 'devin-live';
const WORK = process.argv[5] || path.join(os.homedir(), 'oce/agentwork');
process.env.GHOSTTY_SENDKEYS_RESP_DIR = path.join(path.dirname(SPOOL), 'responses');
const sh = (cmd, args, opts={}) => execFileSync(cmd, args, {encoding:'utf8', timeout: opts.t||600000, maxBuffer: 64*1024*1024, ...opts});
const sendkeys = (...a) => sh('node', [SK, '--dir', SPOOL, ...a]);
const sleep = ms => sh('sleep', [String(ms/1000)]);
const curl = (args) => sh('curl', ['-s', '-m', '60', ...args]);

let quiet = 0;
while (quiet < 120) {
  let out;
  try { out = curl([`${PROXY}/v1/workers/${QUEUE}/jobs?wait=25`]); } catch(e) { quiet++; continue; }
  if (!out.trim()) { quiet++; continue; }
  let job; try { job = JSON.parse(out); } catch(e) { quiet++; continue; }
  quiet = 0;
  const jid = job.id;
  console.log(new Date().toISOString(), 'JOB', jid, 'msgs:', (job.messages||[]).length, 'tools:', job.tools ? JSON.parse(JSON.stringify(job.tools)).length : 0);
  const jf = path.join(WORK, `job-${jid}.md`), af = path.join(WORK, `answer-${jid}.json`);
  let doc = `# Job ${jid}\n\nYou are acting as the LLM brain for ANOTHER coding agent (opencode). opencode executes tools on its side; you only DECIDE. Do NOT do the user's task yourself, do NOT create the user's files yourself.\n\n`;
  if (job.tools) doc += `## AVAILABLE TOOLS (opencode executes these, you may call them)\n\n\`\`\`json\n${JSON.stringify(job.tools, null, 1)}\n\`\`\`\n\n`;
  doc += `## CONVERSATION\n\n`;
  for (const msg of job.messages||[]) {
    let c = msg.content; if (typeof c !== 'string') c = JSON.stringify(c);
    doc += `### ${msg.role}${msg.tool_call_id ? ' (result of '+msg.tool_call_id+')' : ''}\n${c||''}\n`;
    if (msg.tool_calls) doc += `\n[assistant tool_calls]: ${JSON.stringify(msg.tool_calls)}\n`;
    doc += `\n`;
  }
  doc += `## HOW TO ANSWER\nWrite EXACTLY ONE file: ${af}\nIt must contain ONLY valid JSON, one of:\n- {"content": "final text reply to the user"}\n- {"tool_calls": [{"name": "<tool name from AVAILABLE TOOLS>", "arguments": { ...args matching that tool's schema... }}]}\nUse tool_calls whenever the request needs file/shell/edit actions — opencode runs them and will send you the results in the next job. Creating ${af} is your ONLY deliverable for this job.\n`;
  fs.writeFileSync(jf, doc);
  sendkeys('send', `TEXT:New job. Read ${jf} and follow its HOW TO ANSWER section exactly.`, '--wait');
  sleep(600); sendkeys('send', 'KEY:enter', '--wait');
  const t0 = Date.now();
  let lastNudge = Date.now();
  while (!fs.existsSync(af) && Date.now()-t0 < 480000) {
    sleep(3000);
    if (Date.now()-lastNudge > 45000) { // free-plan throttle: re-nudge to retry
      sendkeys('send', `TEXT:Retry: read ${jf} and follow its HOW TO ANSWER section exactly.`, '--wait');
      sleep(600); sendkeys('send', 'KEY:enter', '--wait');
      lastNudge = Date.now();
    }
  }
  if (!fs.existsSync(af)) { console.log('TIMEOUT', jid); continue; }
  sleep(2000);
  let ans;
  try { ans = JSON.parse(fs.readFileSync(af, 'utf8')); }
  catch(e) { ans = {content: fs.readFileSync(af, 'utf8')}; }
  const body = JSON.stringify(ans);
  curl(['-X','POST', `${PROXY}/v1/workers/${QUEUE}/jobs/${jid}`, '-H','Content-Type: application/json', '--data-binary', body]);
  console.log('ANSWERED', jid, ans.tool_calls ? `tool_calls: ${ans.tool_calls.map(t=>t.name).join(',')}` : `text ${String(ans.content||'').length}ch`);
}
console.log('driver done');
