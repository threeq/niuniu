import { useMemo, useState, type ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Database,
  Plus,
  Pencil,
  Trash2,
  Plug,
  Check,
  ChevronsUpDown,
} from 'lucide-react';
import { toast } from 'sonner';
import { confirm } from '@/lib/confirm';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog';
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from '@/components/ui/popover';
import {
  Command,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
} from '@/components/ui/command';
import {
  listDataSources,
  createDataSource,
  updateDataSource,
  removeDataSource,
  verifyDataSource,
  type DataSource,
  type CreateDataSourceBody,
  type ScopeConfig,
  type DataSourceBinding,
  type SSHAuthMethod,
} from '@/lib/data-sources-api';
import { api } from '@/lib/api';
import type { Project } from '@/types/api';
import type { DataSourceKind } from '@/types/data';

// Kind groups, ordered as rendered in the type combobox (#360). Mirrors the
// "two-family" taxonomy of the Epic #345 NoSQL expansion: SQL family first
// (relational / NewSQL / OLAP / query engine), then the per-protocol families.
const KIND_GROUPS = [
  'relational', 'newsql', 'olap', 'engine', 'document', 'keyvalue', 'search', 'http',
] as const;
type KindGroup = (typeof KIND_GROUPS)[number];

interface KindMeta {
  label: string;
  defaultPort: number;
  group: KindGroup;
  /** Search-only keywords (brand aliases, never rendered): "pg" hits every PG-wire kind. */
  aliases?: string[];
}

const KIND_META: Record<DataSourceKind, KindMeta> = {
  mysql:         { label: 'MySQL',         defaultPort: 3306,  group: 'relational' },
  postgres:      { label: 'PostgreSQL',    defaultPort: 5432,  group: 'relational', aliases: ['pg', 'pgsql'] },
  mariadb:       { label: 'MariaDB',       defaultPort: 3306,  group: 'relational', aliases: ['mysql'] },
  mssql:         { label: 'SQL Server',    defaultPort: 1433,  group: 'relational', aliases: ['microsoft'] },
  opengauss:     { label: 'openGauss',     defaultPort: 5432,  group: 'relational', aliases: ['pg', 'postgres', 'gauss'] },
  polardbpg:     { label: 'PolarDB-PG',    defaultPort: 5432,  group: 'relational', aliases: ['pg', 'postgres', 'alibaba'] },
  tidb:          { label: 'TiDB',          defaultPort: 4000,  group: 'newsql',     aliases: ['mysql', 'pingcap'] },
  oceanbase:     { label: 'OceanBase',     defaultPort: 2881,  group: 'newsql',     aliases: ['ob', 'mysql'] },
  cockroachdb:   { label: 'CockroachDB',   defaultPort: 26257, group: 'newsql',     aliases: ['pg', 'postgres', 'crdb'] },
  yugabyte:      { label: 'YugabyteDB',    defaultPort: 5433,  group: 'newsql',     aliases: ['pg', 'postgres', 'yb'] },
  clickhouse:    { label: 'ClickHouse',    defaultPort: 9000,  group: 'olap',       aliases: ['ck', 'ch'] },
  starrocks:     { label: 'StarRocks',     defaultPort: 9030,  group: 'olap',       aliases: ['mysql', 'sr'] },
  doris:         { label: 'Apache Doris',  defaultPort: 9030,  group: 'olap',       aliases: ['mysql'] },
  greenplum:     { label: 'Greenplum',     defaultPort: 5432,  group: 'olap',       aliases: ['pg', 'postgres', 'gp', 'mpp'] },
  redshift:      { label: 'Redshift',      defaultPort: 5439,  group: 'olap',       aliases: ['pg', 'postgres', 'aws', 'amazon'] },
  trino:         { label: 'Trino',         defaultPort: 8080,  group: 'engine',     aliases: ['presto', 'lake', 'federation'] },
  mongo:         { label: 'MongoDB',       defaultPort: 27017, group: 'document',   aliases: ['document', 'doc'] },
  redis:         { label: 'Redis',         defaultPort: 6379,  group: 'keyvalue',   aliases: ['kv', 'cache'] },
  elasticsearch: { label: 'Elasticsearch', defaultPort: 9200,  group: 'search',     aliases: ['es', 'elastic'] },
  http:          { label: 'HTTP API',      defaultPort: 443,   group: 'http',       aliases: ['rest', 'api', 'json', 'webhook'] },
};

const SUPPORTED_KINDS: DataSourceKind[] = [
  'mysql', 'postgres', 'mariadb', 'mssql', 'opengauss', 'polardbpg',
  'tidb', 'oceanbase', 'cockroachdb', 'yugabyte',
  'clickhouse', 'starrocks', 'doris', 'greenplum', 'redshift',
  'trino', 'mongo', 'redis', 'elasticsearch', 'http',
];

// Which connection-field set the form renders; everything not listed uses the
// host/user/password/database SQL layout. Mirrors dataconn.KindFamily, except
// trino is split out here because its SQL config maps catalog→database and
// schema→options.schema.
type FormFamily = 'sql' | 'trino' | 'mongo' | 'redis' | 'elastic' | 'http';

function formFamily(kind: DataSourceKind): FormFamily {
  switch (kind) {
    case 'trino': return 'trino';
    case 'mongo': return 'mongo';
    case 'redis': return 'redis';
    case 'elasticsearch': return 'elastic';
    case 'http': return 'http';
    default: return 'sql';
  }
}

// Parses a mongodb:// URI into the backend config shape — the server rebuilds
// the driver URI from host/port/user/password/database and turns every query
// param into a driver option (dataconn.mongoURI). Returns null when unusable.
// mongodb+srv is rejected: ConnConfig carries one literal host+port, no SRV.
function parseMongoUri(uri: string): CreateDataSourceBody['config'] | null {
  let u: URL;
  try {
    u = new URL(uri.trim());
  } catch {
    return null;
  }
  if (u.protocol !== 'mongodb:' || !u.hostname) return null;
  const options: Record<string, string> = {};
  u.searchParams.forEach((v, k) => {
    options[k] = v;
  });
  const config: CreateDataSourceBody['config'] = {
    host: u.hostname,
    port: u.port ? Number(u.port) : undefined,
    user: decodeURIComponent(u.username),
    database: u.pathname.replace(/^\//, ''),
  };
  if (u.password) config.password = decodeURIComponent(u.password);
  // Always present (even empty): the URI's query params are the authoritative
  // option set — an edit that removes a param must actually remove it
  // server-side (service.mergeRetainedSecrets keeps options wholesale only
  // when the key is absent).
  config.options = options;
  return config;
}

// Rebuilds a display URI from the redacted config for edit mode. The password
// is never returned by the API (leaving it blank keeps the stored one —
// mongoUriEditHint); non-secret options (authSource, replicaSet, ...) ARE
// returned and echoed as query params so a URI edit doesn't drop them.
function mongoUriFromConfig(config: DataSource['config']): string {
  if (!config.host) return '';
  const user = config.user ? `${encodeURIComponent(config.user)}@` : '';
  const port = config.port != null ? `:${config.port}` : '';
  const params = new URLSearchParams();
  Object.entries(config.options ?? {}).forEach(([k, v]) => {
    params.set(k, String(v));
  });
  const qs = params.size > 0 ? `?${params.toString()}` : '';
  return `mongodb://${user}${config.host}${port}/${config.database ?? ''}${qs}`;
}

function csvToList(s: string): string[] {
  return s
    .split(',')
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

function listToCsv(xs?: string[]): string {
  return (xs ?? []).join(', ');
}

// Parses the http headers textarea: blank -> {} (keep stored / send none),
// otherwise a JSON object of string-valued headers. Returns null on invalid
// JSON or a non-object value so canSave can block the save.
function parseHeadersInput(s: string): Record<string, string> | null {
  const trimmed = s.trim();
  if (trimmed === '') return {};
  let v: unknown;
  try {
    v = JSON.parse(trimmed);
  } catch {
    return null;
  }
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return null;
  const out: Record<string, string> = {};
  for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
    out[k] = String(val);
  }
  return out;
}

export function DataSourcesPanel() {
  const { t } = useTranslation('data');
  const { data, isLoading } = useQuery({
    queryKey: ['data-sources'],
    queryFn: listDataSources,
  });
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<DataSource | null>(null);

  const handleAdd = () => {
    setEditing(null);
    setDialogOpen(true);
  };
  const handleEdit = (s: DataSource) => {
    setEditing(s);
    setDialogOpen(true);
  };

  return (
    <section className="flex flex-col gap-3 rounded border border-warm-border p-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-medium">{t('sources.sectionTitle')}</h2>
          <p className="text-xs text-warm-text-muted mt-1">
            {t('sources.sectionDescription')}
          </p>
        </div>
        <Button size="sm" onClick={handleAdd}>
          <Plus className="size-4 mr-1" />
          {t('sources.addSource')}
        </Button>
      </div>

      {isLoading && (
        <div className="text-sm text-warm-text-muted py-2">
          {t('sources.loading')}
        </div>
      )}

      {!isLoading && (!data || data.length === 0) && (
        <div className="text-sm text-warm-text-muted py-2">
          {t('sources.empty')}
        </div>
      )}

      {!isLoading && data && data.length > 0 && (
        <div className="flex flex-col divide-y divide-warm-border/60">
          {data.map((s) => (
            <DataSourceRow key={s.id} source={s} onEdit={() => handleEdit(s)} />
          ))}
        </div>
      )}

      <DataSourceDialog
        open={dialogOpen}
        onOpenChange={(next) => {
          setDialogOpen(next);
          if (!next) setEditing(null);
        }}
        editing={editing}
      />
    </section>
  );
}

interface RowProps {
  source: DataSource;
  onEdit: () => void;
}

function DataSourceRow({ source: s, onEdit }: RowProps) {
  const { t, i18n } = useTranslation('data');
  const queryClient = useQueryClient();

  const verify = useMutation({
    mutationFn: () => verifyDataSource(s.id),
    onSuccess: (res) => {
      if (res.ok) {
        toast.success(t('sources.verifySuccess'));
        queryClient.invalidateQueries({ queryKey: ['data-sources'] });
      } else {
        toast.error(res.message || t('sources.verifyFailed'));
      }
    },
    onError: (e) =>
      toast.error(e instanceof Error ? e.message : t('sources.verifyFailed')),
  });

  const del = useMutation({
    mutationFn: () => removeDataSource(s.id),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['data-sources'] }),
  });

  const handleDelete = async () => {
    if (!(await confirm(t('sources.deleteConfirm', { name: s.name })))) return;
    del.mutate();
  };

  const verifiedLabel = s.last_verified_at
    ? t('sources.lastVerified', {
        time: new Date(s.last_verified_at).toLocaleString(i18n.language),
      })
    : t('sources.neverVerified');

  return (
    <div className="flex items-center gap-3 py-3">
      <Database className="size-5 text-warm-text shrink-0" aria-hidden />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium truncate">{s.name}</span>
          {s.bindings.length === 0 && (
            <span className="shrink-0 rounded bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted">
              {t('sources.unboundBadge')}
            </span>
          )}
        </div>
        <div className="text-xs text-warm-text-muted truncate">
          {s.kind}
          {s.config.host ? ` · ${s.config.host}` : ''}
          {s.config.port ? `:${s.config.port}` : ''}
          {' · '}
          {verifiedLabel}
        </div>
      </div>
      <Button
        size="sm"
        variant="outline"
        disabled={verify.isPending}
        onClick={() => verify.mutate()}
      >
        <Plug className="size-4 mr-1" />
        {verify.isPending ? t('sources.verifying') : t('sources.verify')}
      </Button>
      <Button
        size="sm"
        variant="outline"
        aria-label={t('sources.edit')}
        onClick={onEdit}
      >
        <Pencil className="size-4" />
      </Button>
      <Button
        size="sm"
        variant="ghost"
        aria-label={t('sources.delete')}
        disabled={del.isPending}
        onClick={handleDelete}
      >
        <Trash2 className="size-4" />
      </Button>
    </div>
  );
}

interface KindComboboxProps {
  id?: string;
  value: DataSourceKind;
  onChange: (next: DataSourceKind) => void;
  disabled?: boolean;
}

// Searchable, grouped type picker (shadcn Command in a Popover). Items are
// matched on label + kind id + brand aliases via cmdk's `keywords`, so e.g.
// "pg" finds PostgreSQL / PolarDB-PG / Greenplum and every other PG-wire kind.
function KindCombobox({ id, value, onChange, disabled }: KindComboboxProps) {
  const { t } = useTranslation('data');
  const [open, setOpen] = useState(false);

  const grouped = useMemo(
    () =>
      KIND_GROUPS.map((group) => ({
        group,
        kinds: SUPPORTED_KINDS.filter((k) => KIND_META[k].group === group),
      })).filter((g) => g.kinds.length > 0),
    [],
  );

  return (
    // modal: keeps pointer/scroll interaction sane inside the (modal) Dialog.
    <Popover modal open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          variant="outline"
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          className="w-full justify-between font-normal"
        >
          {KIND_META[value].label}
          <ChevronsUpDown className="size-4 shrink-0 opacity-50" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[var(--radix-popover-trigger-width)] p-0"
      >
        <Command>
          <CommandInput placeholder={t('sources.kindSearchPlaceholder')} />
          <CommandList>
            <CommandEmpty>{t('sources.kindEmpty')}</CommandEmpty>
            {grouped.map(({ group, kinds }) => (
              <CommandGroup key={group} heading={t(`sources.group.${group}`)}>
                {kinds.map((k) => (
                  <CommandItem
                    key={k}
                    value={k}
                    keywords={[KIND_META[k].label, ...(KIND_META[k].aliases ?? [])]}
                    onSelect={(next) => {
                      onChange(next as DataSourceKind);
                      setOpen(false);
                    }}
                  >
                    <Check
                      className={cn(
                        'size-4 shrink-0',
                        value === k ? 'opacity-100' : 'opacity-0',
                      )}
                      aria-hidden
                    />
                    {KIND_META[k].label}
                  </CommandItem>
                ))}
              </CommandGroup>
            ))}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

interface DialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editing: DataSource | null;
}

// The outer wrapper keys the inner form on (open, editing.id) so it fully
// remounts with fresh useState initializers — no setState-in-effect hydration.
function DataSourceDialog({ open, onOpenChange, editing }: DialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh]">
        {open && (
          <DataSourceForm
            key={editing ? `edit-${editing.id}` : 'create'}
            editing={editing}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

interface FormProps {
  editing: DataSource | null;
  onClose: () => void;
}

function DataSourceForm({ editing, onClose }: FormProps) {
  const { t } = useTranslation('data');
  const queryClient = useQueryClient();
  const isEdit = Boolean(editing);

  // Initial state derived from the editing target via useState initializers
  // (the form remounts per-target via the key prop above). Password stays
  // blank in edit mode (config is never returned) — a blank password keeps
  // the current one server-side.
  const [name, setName] = useState(editing?.name ?? '');
  const [kind, setKind] = useState<DataSourceKind>(editing?.kind ?? 'mysql');
  const [host, setHost] = useState(editing?.config.host ?? '');
  const [port, setPort] = useState(
    editing?.config.port != null ? String(editing.config.port) : '',
  );
  const [user, setUser] = useState(editing?.config.user ?? '');
  const [password, setPassword] = useState('');
  // For redis, `database` holds the logical db number; for trino, the catalog.
  const [database, setDatabase] = useState(editing?.config.database ?? '');
  const [mongoUri, setMongoUri] = useState(
    editing?.kind === 'mongo' ? mongoUriFromConfig(editing.config) : '',
  );
  // Trino's default schema lives in config.options.schema (non-secret, echoed
  // by the list response) so edit mode can show and round-trip it.
  const [trinoSchema, setTrinoSchema] = useState(
    editing?.kind === 'trino' ? ((editing.config.options?.schema as string) ?? '') : '',
  );
  // HTTP source: base URL scheme + optional path prefix are non-secret (echoed
  // by the list response); headers carry auth and are redacted, so the textarea
  // starts blank in edit mode (blank = keep stored headers).
  const [httpScheme, setHttpScheme] = useState(
    editing?.kind === 'http' ? ((editing.config.options?.scheme as string) ?? 'https') : 'https',
  );
  const [httpBasePath, setHttpBasePath] = useState(
    editing?.kind === 'http' ? ((editing.config.options?.base_path as string) ?? '') : '',
  );
  const [httpHeaders, setHttpHeaders] = useState('');
  // http "unpack" rule: a dotted path to the list inside the response (e.g.
  // data.list). Blank = auto-detect a common wrapper (data/list/items/...).
  const [httpListPath, setHttpListPath] = useState(
    editing?.kind === 'http' ? ((editing.config.options?.list_path as string) ?? '') : '',
  );
  const [esAuthMode, setEsAuthMode] = useState<'apikey' | 'basic'>(
    editing?.kind === 'elasticsearch' && !editing.config.user
      ? 'apikey'
      : 'basic',
  );
  const [esApiKey, setEsApiKey] = useState('');
  // True once any connection field is touched. Edit PATCHes omit `config`
  // until then: the backend replaces the whole encrypted config when present,
  // which would wipe the stored secrets on a pure rename / scope edit.
  const [connDirty, setConnDirty] = useState(false);

  // SSH tunnel (optional). Secrets (password / private key) are never returned;
  // sshHasStoredKey signals a stored key so the UI shows "keep stored" instead
  // of forcing re-entry. Available for every non-mongo family (mongo carries
  // its own URI; tunnelling it is out of scope for now).
  const editSSH = editing?.config.ssh;
  const [sshEnabled, setSshEnabled] = useState(editSSH?.enabled ?? false);
  const [sshHost, setSshHost] = useState(editSSH?.host ?? '');
  const [sshPort, setSshPort] = useState(
    editSSH?.port != null ? String(editSSH.port) : '',
  );
  const [sshUser, setSshUser] = useState(editSSH?.user ?? '');
  const [sshAuthMethod, setSshAuthMethod] = useState<SSHAuthMethod>(
    editSSH?.auth_method ?? 'private_key',
  );
  const [sshPassword, setSshPassword] = useState('');
  const [sshPrivateKey, setSshPrivateKey] = useState('');
  const [sshHostKey, setSshHostKey] = useState(editSSH?.host_key ?? '');
  const sshHasStoredKey = editSSH?.has_private_key ?? false;

  const [scopeDatabases, setScopeDatabases] = useState(
    listToCsv(editing?.scope_config.databases),
  );
  const [scopeAllow, setScopeAllow] = useState(
    listToCsv(editing?.scope_config.tables_allow),
  );
  const [scopeDeny, setScopeDeny] = useState(
    listToCsv(editing?.scope_config.tables_deny),
  );
  // http-only: allow-list of HTTP verbs (blank = no method restriction).
  const [httpMethods, setHttpMethods] = useState(
    listToCsv(editing?.scope_config.methods),
  );
  const [requireConfirm, setRequireConfirm] = useState<
    CreateDataSourceBody['require_confirm']
  >(editing?.require_confirm ?? 'writes_only');
  // Visibility bindings: a source with none is invisible to every agent.
  const [bindings, setBindings] = useState<DataSourceBinding[]>(
    editing?.bindings ?? [],
  );

  const family = formFamily(kind);

  // Per-kind field handlers funnel through this so any connection edit flips
  // connDirty (see its declaration for why edit PATCHes depend on it).
  const onConnChange =
    (set: (v: string) => void) =>
    (e: ChangeEvent<HTMLInputElement | HTMLSelectElement>) => {
      setConnDirty(true);
      set(e.target.value);
    };

  // When the SSH tunnel is on, the bastion host + user are required, and (on
  // create, or after switching auth) a credential must be supplied unless a key
  // is already stored.
  const sshValid = useMemo(() => {
    if (!sshEnabled || family === 'mongo') return true;
    if (sshHost.trim().length === 0 || sshUser.trim().length === 0) return false;
    if (sshAuthMethod === 'password') {
      return isEdit || sshPassword.length > 0;
    }
    return isEdit ? sshHasStoredKey || sshPrivateKey.trim().length > 0 : sshPrivateKey.trim().length > 0;
  }, [sshEnabled, family, sshHost, sshUser, sshAuthMethod, sshPassword, sshPrivateKey, sshHasStoredKey, isEdit]);

  const canSave = useMemo(() => {
    if (name.trim().length === 0) return false;
    if (!sshValid) return false;
    if (family === 'mongo') return parseMongoUri(mongoUri) !== null;
    if (host.trim().length === 0) return false;
    switch (family) {
      case 'trino':
        // The Trino DSN requires a user even without a password.
        return user.trim().length > 0;
      case 'redis':
        // Logical db is a non-negative number; blank means db 0.
        return database.trim() === '' || /^\d+$/.test(database.trim());
      case 'elastic':
        return isEdit || esAuthMode === 'basic' || esApiKey.length > 0;
      case 'http':
        // Auth is optional (public APIs allowed); only the headers JSON, if
        // provided, must be a valid object.
        return parseHeadersInput(httpHeaders) !== null;
      default:
        return isEdit || password.length > 0;
    }
  }, [name, family, mongoUri, host, user, database, esAuthMode, esApiKey, password, httpHeaders, isEdit, sshValid]);

  // Builds the kind-specific config payload; null when the mongo URI is
  // unparseable (canSave already blocks that path).
  const buildConfig = (): CreateDataSourceBody['config'] | null => {
    if (family === 'mongo') return parseMongoUri(mongoUri);
    const config: CreateDataSourceBody['config'] = {
      host: host.trim(),
      port: port ? Number(port) : undefined,
    };
    // Blank secrets mean "keep what is stored" (service.mergeRetainedSecrets):
    // password is simply omitted; api_key is sent as "" because an ABSENT
    // api_key is the drop signal (switching to basic auth must clear it).
    switch (family) {
      case 'redis':
        config.database = database.trim();
        if (password.length > 0) config.password = password;
        break;
      case 'elastic':
        if (esAuthMode === 'apikey') {
          config.options = { api_key: esApiKey };
        } else {
          config.user = user.trim();
          if (password.length > 0) config.password = password;
          config.options = {}; // authoritative: drops a previously stored api_key
        }
        break;
      case 'http': {
        // headers carry auth (redacted); a blank textarea sends {} which the
        // backend treats as "keep stored" on edit (mergeRetainedSecrets) and
        // "none" on create. scheme/base_path are authoritative.
        const headers = parseHeadersInput(httpHeaders) ?? {};
        const options: Record<string, unknown> = { scheme: httpScheme, headers };
        if (httpBasePath.trim().length > 0) options.base_path = httpBasePath.trim();
        if (httpListPath.trim().length > 0) options.list_path = httpListPath.trim();
        // Optional HTTP basic auth.
        config.user = user.trim();
        if (password.length > 0) config.password = password;
        config.options = options;
        break;
      }
      default:
        // sql + trino; for trino `database` carries the catalog.
        config.user = user.trim();
        config.database = database.trim();
        if (password.length > 0) config.password = password;
        if (family === 'trino') {
          // Always authoritative so clearing the field really clears it.
          config.options = trinoSchema.trim().length > 0 ? { schema: trinoSchema.trim() } : {};
        }
    }
    // SSH tunnel (all non-mongo families). Blank secrets follow the same
    // "leave blank to keep stored" contract as the DB password
    // (service.mergeRetainedSSHSecrets). host_key is authoritative.
    config.ssh = {
      enabled: sshEnabled,
      host: sshHost.trim(),
      port: sshPort ? Number(sshPort) : undefined,
      user: sshUser.trim(),
      auth_method: sshAuthMethod,
      host_key: sshHostKey.trim(),
    };
    if (sshAuthMethod === 'password') {
      if (sshPassword.length > 0) config.ssh.password = sshPassword;
    } else if (sshPrivateKey.trim().length > 0) {
      config.ssh.private_key = sshPrivateKey;
    }
    return config;
  };

  const save = useMutation({
    mutationFn: async () => {
      const scope_config: ScopeConfig = {
        databases: csvToList(scopeDatabases),
        tables_allow: csvToList(scopeAllow),
        tables_deny: csvToList(scopeDeny),
        ...(family === 'http' ? { methods: csvToList(httpMethods) } : {}),
      };
      const config = buildConfig();
      if (!config) throw new Error(t('sources.mongoUriInvalid'));

      if (isEdit && editing) {
        return updateDataSource(editing.id, {
          name: name.trim(),
          // Untouched connection fields → omit config entirely so the stored
          // secrets (password / api_key / options) survive the PATCH.
          ...(connDirty ? { config } : {}),
          scope_config,
          // M1: default access mode locked to read-only.
          default_access_mode: 'read',
          require_confirm: requireConfirm,
          bindings,
        });
      }
      return createDataSource({
        name: name.trim(),
        kind,
        config,
        scope_config,
        default_access_mode: 'read',
        require_confirm: requireConfirm,
        bindings,
      });
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['data-sources'] });
      toast.success(t('sources.saveSuccess'));
      onClose();
    },
    onError: (e) =>
      toast.error(
        t('sources.saveFailed', {
          error: e instanceof Error ? e.message : String(e),
        }),
      ),
  });

  return (
    <>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Database className="size-4" />
            {isEdit ? t('sources.editTitle') : t('sources.createTitle')}
          </DialogTitle>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2">
            <Label htmlFor="ds-name">{t('sources.nameLabel')}</Label>
            <Input
              id="ds-name"
              value={name}
              placeholder={t('sources.namePlaceholder')}
              onChange={(e) => setName(e.target.value)}
            />
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="ds-kind">{t('sources.kindLabel')}</Label>
            {/* Kind is immutable after creation: the PATCH body has no kind
                field (the stored config shape is kind-specific). */}
            <KindCombobox
              id="ds-kind"
              value={kind}
              disabled={isEdit}
              onChange={(next) => {
                setConnDirty(true);
                // Crossing a form family discards family-specific fields:
                // a leftover sql `database` would otherwise silently fail the
                // redis db-number validation with no visible error.
                if (formFamily(next) !== family) {
                  setDatabase('');
                  setUser('');
                  setPassword('');
                  setMongoUri('');
                  setTrinoSchema('');
                  setEsApiKey('');
                  setHttpScheme('https');
                  setHttpBasePath('');
                  setHttpHeaders('');
                  setHttpListPath('');
                }
                setKind(next);
              }}
            />
          </div>

          {family === 'mongo' ? (
            <div className="flex flex-col gap-2">
              <Label htmlFor="ds-mongo-uri">{t('sources.mongoUriLabel')}</Label>
              <Input
                id="ds-mongo-uri"
                value={mongoUri}
                placeholder={t('sources.mongoUriPlaceholder')}
                onChange={onConnChange(setMongoUri)}
              />
              <p className="text-xs text-warm-text-muted">
                {isEdit
                  ? t('sources.mongoUriEditHint')
                  : t('sources.mongoUriHint')}
              </p>
            </div>
          ) : (
            <div className="grid grid-cols-2 gap-3">
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-host">{t('sources.hostLabel')}</Label>
                <Input
                  id="ds-host"
                  value={host}
                  onChange={onConnChange(setHost)}
                />
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-port">{t('sources.portLabel')}</Label>
                <Input
                  id="ds-port"
                  type="number"
                  value={port}
                  placeholder={String(KIND_META[kind]?.defaultPort ?? '')}
                  onChange={onConnChange(setPort)}
                />
              </div>
            </div>
          )}

          {family === 'http' && (
            <>
              <div className="grid grid-cols-2 gap-3">
                <div className="flex flex-col gap-2">
                  <Label htmlFor="ds-http-scheme">
                    {t('sources.httpSchemeLabel')}
                  </Label>
                  <select
                    id="ds-http-scheme"
                    value={httpScheme}
                    onChange={(e) => {
                      setConnDirty(true);
                      setHttpScheme(e.target.value);
                    }}
                    className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
                  >
                    <option value="https">https</option>
                    <option value="http">http</option>
                  </select>
                </div>
                <div className="flex flex-col gap-2">
                  <Label htmlFor="ds-http-basepath">
                    {t('sources.httpBasePathLabel')}
                  </Label>
                  <Input
                    id="ds-http-basepath"
                    value={httpBasePath}
                    placeholder={t('sources.httpBasePathPlaceholder')}
                    onChange={onConnChange(setHttpBasePath)}
                  />
                </div>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-http-headers">
                  {t('sources.httpHeadersLabel')}
                </Label>
                <Textarea
                  id="ds-http-headers"
                  value={httpHeaders}
                  rows={3}
                  placeholder={t('sources.httpHeadersPlaceholder')}
                  onChange={(e) => {
                    setConnDirty(true);
                    setHttpHeaders(e.target.value);
                  }}
                />
                <p className="text-xs text-warm-text-muted">
                  {isEdit
                    ? t('sources.httpHeadersEditHint')
                    : t('sources.httpHeadersHint')}
                </p>
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-http-listpath">
                  {t('sources.httpListPathLabel')}
                </Label>
                <Input
                  id="ds-http-listpath"
                  value={httpListPath}
                  placeholder={t('sources.httpListPathPlaceholder')}
                  onChange={onConnChange(setHttpListPath)}
                />
                <p className="text-xs text-warm-text-muted">
                  {t('sources.httpListPathHint')}
                </p>
              </div>
            </>
          )}

          {family === 'elastic' && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="ds-es-auth">{t('sources.esAuthModeLabel')}</Label>
              <select
                id="ds-es-auth"
                value={esAuthMode}
                onChange={(e) => {
                  setConnDirty(true);
                  setEsAuthMode(e.target.value as 'apikey' | 'basic');
                }}
                className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
              >
                <option value="apikey">{t('sources.esAuthApiKey')}</option>
                <option value="basic">{t('sources.esAuthBasic')}</option>
              </select>
            </div>
          )}

          {family === 'elastic' && esAuthMode === 'apikey' && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="ds-es-apikey">{t('sources.esApiKeyLabel')}</Label>
              <Input
                id="ds-es-apikey"
                type="password"
                value={esApiKey}
                onChange={onConnChange(setEsApiKey)}
              />
              {isEdit && (
                <p className="text-xs text-warm-text-muted">
                  {t('sources.esApiKeyEditHint')}
                </p>
              )}
            </div>
          )}

          {(family === 'sql' ||
            family === 'trino' ||
            family === 'http' ||
            (family === 'elastic' && esAuthMode === 'basic')) && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="ds-user">
                {family === 'http' ? t('sources.httpUserLabel') : t('sources.userLabel')}
              </Label>
              <Input
                id="ds-user"
                value={user}
                onChange={onConnChange(setUser)}
              />
            </div>
          )}

          {family !== 'mongo' &&
            !(family === 'elastic' && esAuthMode === 'apikey') && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-password">
                  {t('sources.passwordLabel')}
                </Label>
                <Input
                  id="ds-password"
                  type="password"
                  value={password}
                  onChange={onConnChange(setPassword)}
                />
                {isEdit && (
                  <p className="text-xs text-warm-text-muted">
                    {t('sources.passwordEditHint')}
                  </p>
                )}
              </div>
            )}

          {family === 'redis' && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="ds-redis-db">{t('sources.redisDbLabel')}</Label>
              <Input
                id="ds-redis-db"
                type="number"
                min={0}
                value={database}
                placeholder="0"
                onChange={onConnChange(setDatabase)}
              />
            </div>
          )}

          {family === 'trino' && (
            <div className="grid grid-cols-2 gap-3">
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-trino-catalog">
                  {t('sources.trinoCatalogLabel')}
                </Label>
                <Input
                  id="ds-trino-catalog"
                  value={database}
                  onChange={onConnChange(setDatabase)}
                />
              </div>
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-trino-schema">
                  {t('sources.trinoSchemaLabel')}
                </Label>
                <Input
                  id="ds-trino-schema"
                  value={trinoSchema}
                  onChange={onConnChange(setTrinoSchema)}
                />
              </div>
            </div>
          )}

          {family === 'sql' && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="ds-database">{t('sources.databaseLabel')}</Label>
              <Input
                id="ds-database"
                value={database}
                onChange={onConnChange(setDatabase)}
              />
            </div>
          )}

          {family !== 'mongo' && (
            <div className="rounded border border-warm-border/60 p-3 flex flex-col gap-3">
              <div className="flex items-center justify-between gap-2">
                <div className="flex flex-col">
                  <Label htmlFor="ds-ssh-enabled" className="text-xs font-medium">
                    {t('sources.sshTitle')}
                  </Label>
                  <p className="text-xs text-warm-text-muted">
                    {t('sources.sshHint')}
                  </p>
                </div>
                <Switch
                  id="ds-ssh-enabled"
                  checked={sshEnabled}
                  onCheckedChange={(v) => {
                    setConnDirty(true);
                    setSshEnabled(v);
                  }}
                />
              </div>

              {sshEnabled && (
                <>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="flex flex-col gap-2">
                      <Label htmlFor="ds-ssh-host">
                        {t('sources.sshHostLabel')}
                      </Label>
                      <Input
                        id="ds-ssh-host"
                        value={sshHost}
                        onChange={onConnChange(setSshHost)}
                      />
                    </div>
                    <div className="flex flex-col gap-2">
                      <Label htmlFor="ds-ssh-port">
                        {t('sources.sshPortLabel')}
                      </Label>
                      <Input
                        id="ds-ssh-port"
                        type="number"
                        value={sshPort}
                        placeholder="22"
                        onChange={onConnChange(setSshPort)}
                      />
                    </div>
                  </div>

                  <div className="flex flex-col gap-2">
                    <Label htmlFor="ds-ssh-user">
                      {t('sources.sshUserLabel')}
                    </Label>
                    <Input
                      id="ds-ssh-user"
                      value={sshUser}
                      onChange={onConnChange(setSshUser)}
                    />
                  </div>

                  <div className="flex flex-col gap-2">
                    <Label htmlFor="ds-ssh-auth">
                      {t('sources.sshAuthLabel')}
                    </Label>
                    <select
                      id="ds-ssh-auth"
                      value={sshAuthMethod}
                      onChange={(e) => {
                        setConnDirty(true);
                        setSshAuthMethod(e.target.value as SSHAuthMethod);
                      }}
                      className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
                    >
                      <option value="private_key">
                        {t('sources.sshAuthPrivateKey')}
                      </option>
                      <option value="password">
                        {t('sources.sshAuthPassword')}
                      </option>
                    </select>
                  </div>

                  {sshAuthMethod === 'password' ? (
                    <div className="flex flex-col gap-2">
                      <Label htmlFor="ds-ssh-password">
                        {t('sources.sshPasswordLabel')}
                      </Label>
                      <Input
                        id="ds-ssh-password"
                        type="password"
                        value={sshPassword}
                        onChange={onConnChange(setSshPassword)}
                      />
                      {isEdit && (
                        <p className="text-xs text-warm-text-muted">
                          {t('sources.passwordEditHint')}
                        </p>
                      )}
                    </div>
                  ) : (
                    <div className="flex flex-col gap-2">
                      <Label htmlFor="ds-ssh-key">
                        {t('sources.sshPrivateKeyLabel')}
                      </Label>
                      <Textarea
                        id="ds-ssh-key"
                        value={sshPrivateKey}
                        rows={4}
                        placeholder={t('sources.sshPrivateKeyPlaceholder')}
                        onChange={(e) => {
                          setConnDirty(true);
                          setSshPrivateKey(e.target.value);
                        }}
                      />
                      <p className="text-xs text-warm-text-muted">
                        {sshHasStoredKey
                          ? t('sources.sshPrivateKeyStoredHint')
                          : t('sources.sshPrivateKeyHint')}
                      </p>
                    </div>
                  )}

                  <div className="flex flex-col gap-2">
                    <Label htmlFor="ds-ssh-hostkey">
                      {t('sources.sshHostKeyLabel')}
                    </Label>
                    <Input
                      id="ds-ssh-hostkey"
                      value={sshHostKey}
                      placeholder="SHA256:..."
                      onChange={onConnChange(setSshHostKey)}
                    />
                    <p className="text-xs text-warm-text-muted">
                      {t('sources.sshHostKeyHint')}
                    </p>
                  </div>
                </>
              )}
            </div>
          )}

          <BindingEditor bindings={bindings} onChange={setBindings} />

          <div className="rounded border border-warm-border/60 p-3 flex flex-col gap-3">
            <h3 className="text-xs font-medium text-warm-text">
              {t('sources.scopeTitle')}
            </h3>
            {/* databases scope does not apply to http (path-prefix scope). */}
            {family !== 'http' && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-scope-db">
                  {t('sources.scopeDatabasesLabel')}
                </Label>
                <Input
                  id="ds-scope-db"
                  value={scopeDatabases}
                  onChange={(e) => setScopeDatabases(e.target.value)}
                />
                {family === 'trino' && (
                  <p className="text-xs text-warm-text-muted">
                    {t('sources.scopeDatabasesTrinoHint')}
                  </p>
                )}
              </div>
            )}
            <div className="flex flex-col gap-2">
              <Label htmlFor="ds-scope-allow">
                {t('sources.scopeAllowLabel')}
              </Label>
              <Input
                id="ds-scope-allow"
                value={scopeAllow}
                onChange={(e) => setScopeAllow(e.target.value)}
              />
              {family === 'http' && (
                <p className="text-xs text-warm-text-muted">
                  {t('sources.scopeHttpHint')}
                </p>
              )}
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="ds-scope-deny">
                {t('sources.scopeDenyLabel')}
              </Label>
              <Input
                id="ds-scope-deny"
                value={scopeDeny}
                onChange={(e) => setScopeDeny(e.target.value)}
              />
            </div>
            {family === 'http' && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ds-scope-methods">
                  {t('sources.scopeMethodsLabel')}
                </Label>
                <Input
                  id="ds-scope-methods"
                  value={httpMethods}
                  placeholder="GET, POST"
                  onChange={(e) => setHttpMethods(e.target.value)}
                />
                <p className="text-xs text-warm-text-muted">
                  {t('sources.scopeMethodsHint')}
                </p>
              </div>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="ds-access">{t('sources.accessModeLabel')}</Label>
            {/* M1: read-only only — the select is locked. */}
            <select
              id="ds-access"
              value="read"
              disabled
              className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm text-warm-text-muted"
            >
              <option value="read">{t('sources.accessModeRead')}</option>
            </select>
            <p className="text-xs text-warm-text-muted">
              {t('sources.accessModeM1Note')}
            </p>
          </div>

          <div className="flex flex-col gap-2">
            <Label htmlFor="ds-confirm">
              {t('sources.requireConfirmLabel')}
            </Label>
            <select
              id="ds-confirm"
              value={requireConfirm}
              onChange={(e) =>
                setRequireConfirm(
                  e.target.value as CreateDataSourceBody['require_confirm'],
                )
              }
              className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
            >
              <option value="always">{t('sources.confirmAlways')}</option>
              <option value="writes_only">
                {t('sources.confirmWritesOnly')}
              </option>
              <option value="never">{t('sources.confirmNever')}</option>
            </select>
          </div>
        </div>

        <DialogFooter>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={save.isPending}
          >
            {t('sources.cancel')}
          </Button>
          <Button onClick={() => save.mutate()} disabled={!canSave || save.isPending}>
            {save.isPending ? t('sources.saving') : t('sources.save')}
          </Button>
        </DialogFooter>
    </>
  );
}

interface BindingEditorProps {
  bindings: DataSourceBinding[];
  onChange: (next: DataSourceBinding[]) => void;
}

// BindingEditor manages a source's visibility — which PROJECTS may see it. A
// source is exposed to an agent only through the project its workspace belongs
// to (exactly like external sources). An empty list means the source is
// invisible to every agent; the hint makes that explicit.
function BindingEditor({ bindings, onChange }: BindingEditorProps) {
  const { t } = useTranslation('data');

  const projects = useQuery({
    queryKey: ['projects', 'active'],
    queryFn: () => api.get<Project[]>('/projects', { params: { status: 'active' } }),
  });

  const labelFor = (b: DataSourceBinding): string => {
    const hit = (projects.data ?? []).find((p) => p.id === b.target_id);
    return hit ? hit.name : `#${b.target_id}`;
  };

  // Projects not already bound.
  const available = (projects.data ?? []).filter(
    (p) => !bindings.some((b) => b.target_id === p.id),
  );

  const add = (id: number) => {
    if (!id) return;
    if (bindings.some((b) => b.target_id === id)) return;
    onChange([...bindings, { target_type: 'project', target_id: id }]);
  };

  const remove = (b: DataSourceBinding) =>
    onChange(bindings.filter((x) => x.target_id !== b.target_id));

  return (
    <div className="rounded border border-warm-border/60 p-3 flex flex-col gap-3">
      <div>
        <h3 className="text-xs font-medium text-warm-text">
          {t('sources.bindingsTitle')}
        </h3>
        <p className="text-xs text-warm-text-muted mt-1">
          {t('sources.bindingsHint')}
        </p>
      </div>

      {bindings.length === 0 ? (
        <p className="text-xs text-warm-text-muted">
          {t('sources.bindingsEmpty')}
        </p>
      ) : (
        <ul className="flex flex-col gap-1">
          {bindings.map((b) => (
            <li
              key={`${b.target_type}:${b.target_id}`}
              className="flex items-center gap-2 text-sm"
            >
              <span className="shrink-0 rounded bg-warm-muted px-1.5 py-0.5 text-xs text-warm-text-muted">
                {t('sources.bindingTypeProject')}
              </span>
              <span className="flex-1 min-w-0 truncate">{labelFor(b)}</span>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                aria-label={t('sources.bindingRemove')}
                onClick={() => remove(b)}
              >
                <Trash2 className="size-4" />
              </Button>
            </li>
          ))}
        </ul>
      )}

      <select
        aria-label={t('sources.bindingAdd')}
        value=""
        onChange={(e) => {
          if (e.target.value) add(Number(e.target.value));
        }}
        className="h-9 rounded-md border border-input bg-background px-3 py-1 text-sm"
      >
        <option value="">{t('sources.bindingSelectTarget')}</option>
        {available.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
    </div>
  );
}
