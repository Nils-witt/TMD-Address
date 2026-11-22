import {
  Autocomplete,
  Checkbox,
  Chip,
  FormControl,
  FormControlLabel,
  FormHelperText,
  FormLabel,
  Radio,
  RadioGroup,
  TextField,
} from '@mui/material';
import { useTranslation } from 'react-i18next';
import { isSourceSelectionValid, type SourceSelection } from './sourceSelection';
import './SourceFilterFields.scss';

/** Picks manual rows and collectors. Collector IDs can be typed as well as
 * picked, since only admins can list the collectors, and a file may hold the
 * rows of a collector this server doesn't have. */
export default function SourceFilterFields({
  id,
  value,
  onChange,
  collectorOptions,
  disabled,
}: {
  /** Prefix of the fields' IDs, unique on the page. */
  id: string;
  value: SourceSelection;
  onChange: (value: SourceSelection) => void;
  collectorOptions: string[];
  disabled?: boolean;
}) {
  const { t } = useTranslation();
  const invalid = !isSourceSelectionValid(value);

  return (
    <FormControl className="source-filter-fields" disabled={disabled} error={invalid}>
      <FormLabel id={`${id}-scope`}>{t('transfer.sources')}</FormLabel>
      <RadioGroup
        row
        aria-labelledby={`${id}-scope`}
        value={value.scope}
        onChange={(e) => onChange({ ...value, scope: e.target.value as SourceSelection['scope'] })}
      >
        <FormControlLabel value="all" control={<Radio />} label={t('transfer.allSources')} />
        <FormControlLabel
          value="selected"
          control={<Radio />}
          label={t('transfer.selectedSources')}
        />
      </RadioGroup>
      {value.scope === 'selected' && (
        <div className="source-filter-fields__sources">
          <FormControlLabel
            control={
              <Checkbox
                checked={value.manual}
                onChange={(e) => onChange({ ...value, manual: e.target.checked })}
              />
            }
            label={t('transfer.manualData')}
          />
          <Autocomplete
            multiple
            freeSolo
            disabled={disabled}
            options={collectorOptions}
            value={value.collectors}
            onChange={(_, collectors) =>
              onChange({
                ...value,
                collectors: [...new Set(collectors.map((c) => c.trim()).filter(Boolean))],
              })
            }
            renderValue={(selected, getItemProps) =>
              selected.map((option, index) => {
                const { key, ...itemProps } = getItemProps({ index });
                return <Chip key={key} label={option} size="small" {...itemProps} />;
              })
            }
            renderInput={(params) => (
              <TextField
                {...params}
                id={`${id}-collectors`}
                label={t('transfer.collectors')}
                placeholder={t('transfer.collectorsPlaceholder')}
                size="small"
              />
            )}
          />
          <FormHelperText>
            {invalid ? t('transfer.chooseSource') : t('transfer.dependentRowsHint')}
          </FormHelperText>
        </div>
      )}
    </FormControl>
  );
}
