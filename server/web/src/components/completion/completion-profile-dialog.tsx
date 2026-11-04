import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { intakeApi, type CompletionProfile } from '@/lib/intake-api'
import { CompletionPolicyFields } from './completion-policy-fields'
import {
  NEW_PROFILE_DRAFT,
  draftFromPolicy,
  isPolicyDraftValid,
  policyValuesFromDraft,
  type CompletionPolicyDraft,
} from './completion-policy-draft'

interface ProfileFormProps {
  projectId: number
  /** null = create a new profile. */
  profile: CompletionProfile | null
  onDone: () => void
}

// Rendered inside DialogContent, which unmounts when the dialog closes — so
// each open starts from the current profile instead of a stale draft.
function ProfileForm({ projectId, profile, onDone }: ProfileFormProps) {
  const { t } = useTranslation('intake')
  const qc = useQueryClient()
  const [name, setName] = useState(profile?.name ?? '')
  const [nameTouched, setNameTouched] = useState(false)
  const [draft, setDraft] = useState<CompletionPolicyDraft>(() =>
    profile ? draftFromPolicy(profile) : NEW_PROFILE_DRAFT,
  )

  const saveMut = useMutation({
    mutationFn: () => {
      const values = policyValuesFromDraft(draft)
      const body = { name: name.trim(), ...values }
      return profile
        ? intakeApi.updateCompletionProfile(projectId, profile.id, body)
        : intakeApi.createCompletionProfile(projectId, body)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['completion-profiles', projectId] })
      qc.invalidateQueries({ queryKey: ['completion-settings', projectId] })
      toast.success(t('completion.profileSaved'))
      onDone()
    },
    onError: (err: unknown) => {
      const message = err instanceof Error ? err.message : String(err)
      toast.error(t('completion.profileSaveFailed', { message }))
    },
  })

  const nameValid = name.trim().length > 0
  const canSave = nameValid && isPolicyDraftValid(draft) && !saveMut.isPending

  return (
    <>
      <div className="space-y-4">
        <div className="space-y-1.5">
          <Label htmlFor="completion-profile-name">{t('completion.profileName')}</Label>
          <Input
            id="completion-profile-name"
            value={name}
            placeholder={t('completion.profileNamePlaceholder')}
            onChange={(e) => setName(e.target.value)}
            onBlur={() => setNameTouched(true)}
            aria-invalid={nameTouched && !nameValid}
          />
          {nameTouched && !nameValid ? (
            <p className="text-xs text-destructive-text">{t('completion.profileNameRequired')}</p>
          ) : null}
        </div>
        <CompletionPolicyFields
          draft={draft}
          onChange={setDraft}
          idPrefix="completion-profile"
          disabled={saveMut.isPending}
        />
      </div>

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone}>
          {t('completion.cancel')}
        </Button>
        <Button type="button" onClick={() => saveMut.mutate()} disabled={!canSave}>
          {saveMut.isPending ? t('completion.saving') : t('completion.save')}
        </Button>
      </DialogFooter>
    </>
  )
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  projectId: number
  profile: CompletionProfile | null
}

export function CompletionProfileDialog({ open, onOpenChange, projectId, profile }: Props) {
  const { t } = useTranslation('intake')

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {profile ? t('completion.editProfile') : t('completion.newProfile')}
          </DialogTitle>
          <DialogDescription>{t('completion.profileDialogDescription')}</DialogDescription>
        </DialogHeader>
        <ProfileForm
          key={profile?.id ?? 'new'}
          projectId={projectId}
          profile={profile}
          onDone={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  )
}
