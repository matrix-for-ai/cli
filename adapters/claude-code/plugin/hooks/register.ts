import type { Register } from 'claude-code'

// The door into this session, with no configuration: it keeps `matrix-for-ai-cli listen` open
// for the session's id, and whatever an entity's handler hands it (`deliver --id <id>`) becomes
// a prompt, or a command run when the handler asked for one. Which session gets a notice, and which
// commands anyone may ask for, is the handler's call; this mod decides nothing.

type Delivery = {
  identity: string
  room: string
  sender?: string
  unread?: number
  reason?: string
  reply_if_stopped?: boolean
  command?: string
}

const CLIENT = 'matrix-for-ai-cli'

let listener: AsyncGenerator<unknown, unknown> | null = null
const waiting = new Map<string, Delivery>() // refused (stopped with Esc), by room; told when a turn completes
const commands: Delivery[] = [] // refused commands, run when a turn completes
let turnId: string | undefined // the main turn running, for esc
const moved: string[] = [] // the Bash commands an abort sent to the background
let stoppedTold = false // one "stopped" reply per stop, not one per notice
let statusAt = 0 // when the status line was last set
let turnAt = 0 // when the current main turn started

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const started = await next(e)
    $.ui.status(undefined) // a reload must not leave a line nobody tracks
    void listen($, await $.session.id())
    return started
  })

  on('session.end', async ($, e, next) => {
    const ended = await next(e)
    void follow($, e.sessionId)
    return ended
  })

  on('turn.start', async ($, e, next) => {
    if (e.agentId === undefined) {
      turnAt = await $.clock.now()
      turnId = e.turnId
    }
    return next(e)
  })

  on('tool.call', { tool: 'Bash' }, async ($, e, next) => {
    const r: any = await next(e)
    if (r.result?.backgroundedByTurnAbort && r.result.backgroundTaskId) moved.push(r.result.backgroundTaskId)
    return r
  })

  on('turn.complete', async ($, e, next) => {
    if (e.agentId === undefined) turnId = undefined
    // A status line lasts until a whole main turn has run with it on screen.
    if (e.agentId === undefined && statusAt && statusAt < turnAt) {
      $.ui.status(undefined)
      statusAt = 0
    }
    // In the order they came: the notices, then the commands (which wait for that turn to end).
    if (waiting.size) {
      const ds = [...waiting.values()]
      waiting.clear()
      void submit($, ds)
    }
    for (const d of commands.splice(0)) void run($, d, false)
    return next(e)
  })
}

// listen keeps the client's listener for this session, replacing the one it had.
async function listen($: any, session: string) {
  await listener?.return(undefined)
  const child = $.process.spawn({ argv: [CLIENT, 'listen', '--id', session] })
  listener = child
  let buf = ''
  try {
    for await (const { stream, text } of child) {
      if (stream !== 'stdout') continue
      buf += text
      let nl: number
      while ((nl = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, nl)
        buf = buf.slice(nl + 1)
        let d: Delivery
        try {
          d = JSON.parse(line) as Delivery
        } catch {
          continue // not a delivery
        }
        void (d.command ? run($, d) : submit($, [d]))
      }
    }
  } catch (err) {
    const why = String((err as Error)?.message ?? err)
    $.ui.log(`${CLIENT} listen: ${why}`, { to: 'debug' })
    // Not setStatus: nothing is listening, so no turn should clear it.
    $.ui.status(`Matrix: not listening, ${CLIENT} did not start (is it in this session's PATH?): ${why}`)
  }
}

// follow moves the listener to the session that took over in this process (/clear, /resume): the mod
// lives in the process, and no session.start comes for it. After an exit the id stays and nothing moves.
async function follow($: any, ended: string) {
  for (let i = 0; i < 30; i++) {
    const session = await $.session.id()
    if (session !== ended) return listen($, session)
    await $.clock.sleep(100)
  }
}

// prompt: one notice says where; several that waited point at `unread`, which lists them all.
function prompt(ds: Delivery[]): string {
  const ids = [...new Set(ds.map(d => d.identity))].join(', ')
  if (ds.length === 1) {
    const d = ds[0]
    return `Matrix: ${d.unread ?? 1} unread from ${d.sender || 'someone'} in ${d.room}. ` +
      `Read and reply as ${ids} with ${CLIENT} (--help).`
  }
  return `Matrix: unread in ${ds.length} rooms (${ds.map(d => d.room).join(', ')}). ` +
    `See them with \`${CLIENT} unread\`, then read and reply as ${ids} (--help).`
}

async function submit($: any, ds: Delivery[]) {
  let reason = ''
  try {
    const r = await $.prompt.submit({ text: prompt(ds) })
    reason = r?.drop ?? ''
  } catch (err) {
    reason = String((err as Error)?.message ?? err)
  }
  if (!reason) {
    stoppedTold = false
    if (!commands.length) setStatus($, undefined)
    return
  }
  for (const d of ds) waiting.set(d.room, d)
  setStatus($, 'Matrix: unread in ' + [...waiting.keys()].join(', '))
  const d = ds.find(d => d.reply_if_stopped)
  if (d && !stoppedTold) {
    stoppedTold = true
    await $.process.run([CLIENT, 'send', '--as', d.identity, d.room,
      "I'm stopped at my terminal (Esc). I'll read this once someone types there."], { timeoutMs: 20_000 })
      .catch(() => undefined)
  }
}

// run: a command the handler asked for (it decided who may), as if typed here; queued while busy.
// esc is not a command but the key.
async function run($: any, d: Delivery, retry = true) {
  try {
    if (d.command === 'esc') return await esc($, d)
    await $.command.run({ command: d.command })
  } catch (err) {
    if (retry) commands.push(d) // once: stopped now, a turn later it runs; refused again, it is not coming
    setStatus($, `Matrix: /${d.command} ${retry ? 'waiting' : 'failed'} (${String((err as Error)?.message ?? err)})`)
  }
}

// esc ends the running turn, if there is one, and what it was running: an abort alone leaves a Bash
// alive in the background. Not from the tool.call hook: TaskStop does nothing from in there.
async function esc($: any, d: Delivery) {
  if (!turnId) return
  moved.length = 0
  await $.turn.abort({ turnId })
  await $.clock.sleep(1000)
  for (const task of moved.splice(0)) {
    const r = await $.tool.call({ tool: 'TaskStop', task_id: task }).catch((err: Error) => ({ deny: err.message }))
    if (!r.deny && !r.isError) continue
    setStatus($, `Matrix: /esc left ${task} running`)
    await $.process.run([CLIENT, 'send', '--as', d.identity, d.room,
      `Turn stopped, but a command is still running (${task}): ${r.deny ?? r.text}`], { timeoutMs: 20_000 })
      .catch(() => undefined)
  }
}



async function setStatus($: any, text: string | undefined) {
  $.ui.status(text)
  statusAt = text ? await $.clock.now() : 0
}
