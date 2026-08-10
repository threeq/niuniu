import { useEffect, useState } from 'react';
import { Folder, ChevronUp, Home, Loader2 } from 'lucide-react';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { listDirs, type FsDir } from '@/lib/fs-api';

type TFn = (k: string) => string;

// DirectoryPicker browses the local server's filesystem (personal edition) so
// the user can pick a knowledge-base folder; the chosen absolute path is handed
// back via onSelect. Browsers can't reveal absolute paths, hence server-side.
export function DirectoryPicker({
  open,
  onOpenChange,
  onSelect,
  t,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSelect: (path: string) => void;
  t: TFn;
}) {
  const [path, setPath] = useState('');
  const [parent, setParent] = useState('');
  const [home, setHome] = useState('');
  const [dirs, setDirs] = useState<FsDir[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);
  // Virtual levels (e.g. the Windows "This PC" drive list) aren't selectable.
  const isVirtual = path.startsWith('::');

  function load(p?: string) {
    setLoading(true);
    setError(false);
    listDirs(p)
      .then((r) => {
        setPath(r.path);
        setParent(r.parent);
        setHome(r.home);
        setDirs(r.dirs);
      })
      .catch(() => setError(true))
      .finally(() => setLoading(false));
  }

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- async fetch: the synchronous loading flag before the promise resolves is the intended pattern
    if (open) load();
  }, [open]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('picker.title')}</DialogTitle>
        </DialogHeader>

        <div className="flex items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 shrink-0"
            disabled={!parent || loading}
            onClick={() => load(parent)}
            aria-label={t('picker.up')}
            title={t('picker.up')}
          >
            <ChevronUp className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 shrink-0"
            disabled={loading}
            onClick={() => load(home)}
            aria-label={t('picker.home')}
            title={t('picker.home')}
          >
            <Home className="h-4 w-4" />
          </Button>
          <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground" title={path}>
            {isVirtual ? t('picker.drives') : path || '…'}
          </span>
        </div>

        <div className="h-64 overflow-y-auto rounded-md border border-border">
          {loading ? (
            <div className="flex h-full items-center justify-center text-muted-foreground">
              <Loader2 className="h-5 w-5 animate-spin" />
            </div>
          ) : error ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-destructive">
              {t('picker.error')}
            </div>
          ) : dirs.length === 0 ? (
            <div className="flex h-full items-center justify-center px-4 text-center text-xs text-muted-foreground">
              {t('picker.empty')}
            </div>
          ) : (
            <ul>
              {dirs.map((d) => (
                <li key={d.path}>
                  <button
                    type="button"
                    onClick={() => load(d.path)}
                    className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-foreground hover:bg-accent"
                    title={d.path}
                  >
                    <Folder className="h-4 w-4 shrink-0 text-info" />
                    <span className="truncate">{d.name}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('picker.cancel')}
          </Button>
          <Button onClick={() => onSelect(path)} disabled={!path || loading || isVirtual}>
            {t('picker.select')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
