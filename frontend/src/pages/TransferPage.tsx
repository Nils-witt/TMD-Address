import { useCollectors } from '../hooks/useCollectors';
import ExportPanel from './transfer-page/ExportPanel';
import ImportPanel from './transfer-page/ImportPanel';
import './transfer-page/TransferPage.scss';

export default function TransferPage() {
  // Only admins can list collectors; for everyone else the IDs are typed in.
  const { data: collectors } = useCollectors();
  const collectorOptions = collectors.map((c) => c.id);

  return (
    <div className="transfer-page">
      <ExportPanel collectorOptions={collectorOptions} />
      <ImportPanel collectorOptions={collectorOptions} />
    </div>
  );
}
