export interface WorkspaceAgent {
  id: number;
  workspace_id: number;
  agent_id: number;
  agent_name: string;
  role: 'coordinator' | 'worker';
  capabilities: string;
  source: 'manual' | 'team' | 'harness_phase';
  source_ref_id: number | null;
  driver: string;
  session_id: string | null;
  execution_mode: 'sequential' | 'parallel';
  status: 'idle' | 'active' | 'error' | 'exited';
  joined_at: string;
  exited_at: string | null;
}

export interface BlackboardEntry {
  id: number;
  workspace_id: number;
  producer_agent: string;
  entry_type: string;
  entry_key: string;
  content: string;
  metadata: string;
  ref_path: string | null;
  harness_run_id: number | null;
  phase_id: number | null;
  created_at: string;
}

export type AgentSource = 'manual' | 'team' | 'harness_phase' | 'subagent'

export interface DispatchAgent {
  id: number
  name: string
  source: AgentSource
  status: 'idle' | 'active' | 'exited' | 'error'
  subagent_type?: string
  subagent_description?: string
  dispatch_tool_use_id?: string
  activity?: AgentActivity
  children: DispatchAgent[]
  joined_at: string
  exited_at?: string
  last_error?: string
}

export interface AgentActivity {
  kind: 'tool_use' | 'text' | 'inbox_sent' | 'inbox_received'
      | 'blackboard_write' | 'dispatched' | 'thinking' | 'tool_result' | 'done' | 'error'
  tool_name?: string
  target?: string
  text_preview?: string
  occurred_at: string
}

export interface DispatchTree {
  agents: DispatchAgent[]
}

export interface InboxEdgePulse {
  from_agent_id: number
  to_agent_id: number
  subject: string
  at: number // Date.now() for auto-expiry
}
