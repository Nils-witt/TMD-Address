import { useState } from 'react';
import { Button, Paper, Typography } from '@mui/material';
import DownloadIcon from '@mui/icons-material/Download';
import { useTranslation } from 'react-i18next';
import ErrorBanner from '../../components/ErrorBanner';
import SourceFilterFields from '../../features/transfer/SourceFilterFields';
import {
  ALL_SOURCES,
  isSourceSelectionValid,
  toSourceFilter,
  type SourceSelection,
} from '../../features/transfer/sourceSelection';
import { useTransfer } from '../../hooks/useTransfer';
import { errorMessage } from '../../lib/errors';

export default function ExportPanel({ collectorOptions }: { collectorOptions: string[] }) {
  const { t } = useTranslation();
  const { downloadExport } = useTransfer();
  const [selection, setSelection] = useState<SourceSelection>(ALL_SOURCES);
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const onExport = async () => {
    setError(null);
    setExporting(true);
    try {
      await downloadExport(toSourceFilter(selection));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setExporting(false);
    }
  };

  return (
    <Paper className="transfer-panel">
      <div>
        <Typography variant="h6" component="h1">
          {t('transfer.exportTitle')}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          {t('transfer.exportIntro')}
        </Typography>
      </div>
      <ErrorBanner message={error} />
      <SourceFilterFields
        id="export"
        value={selection}
        onChange={setSelection}
        collectorOptions={collectorOptions}
        disabled={exporting}
      />
      <div className="transfer-panel__actions">
        <Button
          variant="contained"
          startIcon={<DownloadIcon />}
          disabled={exporting || !isSourceSelectionValid(selection)}
          onClick={() => void onExport()}
        >
          {exporting ? t('transfer.exporting') : t('transfer.export')}
        </Button>
      </div>
    </Paper>
  );
}
