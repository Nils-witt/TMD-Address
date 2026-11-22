import { useState } from 'react';
import {
  Button,
  FormControl,
  FormControlLabel,
  FormLabel,
  Paper,
  Radio,
  RadioGroup,
  Typography,
} from '@mui/material';
import UploadFileIcon from '@mui/icons-material/UploadFile';
import { useTranslation } from 'react-i18next';
import type { ImportMode, ImportResult } from '../../api/types';
import ErrorBanner from '../../components/ErrorBanner';
import SourceFilterFields from '../../features/transfer/SourceFilterFields';
import {
  ALL_SOURCES,
  isSourceSelectionValid,
  toSourceFilter,
  type SourceSelection,
} from '../../features/transfer/sourceSelection';
import { useConfirm } from '../../hooks/useConfirm';
import { useTransfer } from '../../hooks/useTransfer';
import { errorMessage } from '../../lib/errors';
import ImportResultTable from './ImportResultTable';

export default function ImportPanel({ collectorOptions }: { collectorOptions: string[] }) {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const { importFile } = useTransfer();
  const [file, setFile] = useState<File | null>(null);
  const [selection, setSelection] = useState<SourceSelection>(ALL_SOURCES);
  const [mode, setMode] = useState<ImportMode>('skip');
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  // A result is only true of the file and options it was computed for.
  const changed = <T,>(set: (value: T) => void) => {
    return (value: T) => {
      set(value);
      setResult(null);
    };
  };

  const run = async (dryRun: boolean) => {
    if (!file) return;
    if (!dryRun) {
      const ok = await confirm({
        title: t('transfer.confirmTitle'),
        message: t('transfer.confirmMessage', { name: file.name }),
        confirmLabel: t('transfer.import'),
      });
      if (!ok) return;
    }
    setError(null);
    setBusy(true);
    try {
      setResult(await importFile(file, { filter: toSourceFilter(selection), mode, dryRun }));
    } catch (err) {
      setResult(null);
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const ready = file !== null && !busy && isSourceSelectionValid(selection);

  return (
    <Paper className="transfer-panel">
      <div>
        <Typography variant="h6" component="h1">
          {t('transfer.importTitle')}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          {t('transfer.importIntro')}
        </Typography>
      </div>
      <ErrorBanner message={error} />
      <div className="transfer-panel__actions">
        <Button component="label" variant="outlined" startIcon={<UploadFileIcon />} disabled={busy}>
          {t('transfer.chooseFile')}
          <input
            type="file"
            accept="application/json,.json"
            hidden
            onChange={(e) => {
              changed(setFile)(e.target.files?.[0] ?? null);
              // Lets the same file be chosen again after it was edited.
              e.target.value = '';
            }}
          />
        </Button>
        <Typography variant="body2" className="transfer-panel__file-name">
          {file ? file.name : t('transfer.noFile')}
        </Typography>
      </div>
      <SourceFilterFields
        id="import"
        value={selection}
        onChange={changed(setSelection)}
        collectorOptions={collectorOptions}
        disabled={busy}
      />
      <FormControl disabled={busy}>
        <FormLabel id="import-mode">{t('transfer.mode')}</FormLabel>
        <RadioGroup
          aria-labelledby="import-mode"
          value={mode}
          onChange={(e) => changed(setMode)(e.target.value as ImportMode)}
        >
          <FormControlLabel value="skip" control={<Radio />} label={t('transfer.modeSkip')} />
          <FormControlLabel value="update" control={<Radio />} label={t('transfer.modeUpdate')} />
        </RadioGroup>
      </FormControl>
      <div className="transfer-panel__actions">
        <Button variant="outlined" disabled={!ready} onClick={() => void run(true)}>
          {t('transfer.preview')}
        </Button>
        <Button variant="contained" disabled={!ready} onClick={() => void run(false)}>
          {busy ? t('transfer.importing') : t('transfer.import')}
        </Button>
      </div>
      {result && <ImportResultTable result={result} />}
    </Paper>
  );
}
