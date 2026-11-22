import { Table, TableBody, TableCell, TableHead, TableRow, Typography } from '@mui/material';
import { useTranslation } from 'react-i18next';
import type { ImportEntityStats, ImportResult } from '../../api/types';

const COLUMNS: (keyof ImportEntityStats)[] = ['created', 'updated', 'unchanged', 'skipped'];

/** What an import did, or would do, per kind of row. */
export default function ImportResultTable({ result }: { result: ImportResult }) {
  const { t } = useTranslation();
  const rows: { label: string; stats: ImportEntityStats }[] = [
    { label: t('nav.streets'), stats: result.streets },
    { label: t('nav.houseNumbers'), stats: result.houseNumbers },
    { label: t('nav.geoObjects'), stats: result.geoObjects },
  ];

  return (
    <div>
      <Typography variant="subtitle1" component="h2">
        {result.dryRun ? t('transfer.previewResult') : t('transfer.importResult')}
      </Typography>
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell />
            {COLUMNS.map((column) => (
              <TableCell key={column} align="right">
                {t(`transfer.stats.${column}`)}
              </TableCell>
            ))}
          </TableRow>
        </TableHead>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={row.label}>
              <TableCell component="th" scope="row">
                {row.label}
              </TableCell>
              {COLUMNS.map((column) => (
                <TableCell key={column} align="right">
                  {row.stats[column]}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <Typography variant="body2" color="text.secondary">
        {t('transfer.skippedHint')}
      </Typography>
    </div>
  );
}
