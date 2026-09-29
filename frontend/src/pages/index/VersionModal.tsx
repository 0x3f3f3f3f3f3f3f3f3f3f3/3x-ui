import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Alert, Button, Collapse, Modal, Spin, Tag, Tooltip } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';

import { HttpUtil } from '@/utils';
import { activateOnKey } from '@/utils/a11y';
import type { Status } from '@/models/status';
import GeodataSection from './GeodataSection';
import './VersionModal.css';

interface BusyEvent {
  busy: boolean;
  tip?: string;
}

interface ManagedCoreRelease {
  tag: string;
  prerelease: boolean;
}

interface VersionModalProps {
  open: boolean;
  status: Status;
  onClose: () => void;
  onBusy: (e: BusyEvent) => void;
}

const GEOFILES = [
  'geosite.dat',
  'geoip.dat',
  'geosite_IR.dat',
  'geoip_IR.dat',
  'geosite_RU.dat',
  'geoip_RU.dat',
];

export default function VersionModal({ open, status, onClose, onBusy }: VersionModalProps) {
  const { t } = useTranslation();
  const [modal, modalContextHolder] = Modal.useModal();
  const [activeKey, setActiveKey] = useState<string | string[]>('1');
  const [versions, setVersions] = useState<ManagedCoreRelease[]>([]);
  const [catalogError, setCatalogError] = useState('');
  const [loading, setLoading] = useState(false);

  const [wasOpen, setWasOpen] = useState(false);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setLoading(true);
      setVersions([]);
      setCatalogError('');
    }
  }

  useEffect(() => {
    if (!open) return;
    let current = true;
    void HttpUtil.get<ManagedCoreRelease[]>('/panel/api/server/getManagedCoreReleases')
      .then((msg) => {
        if (!current) return;
        if (msg?.success) setVersions(msg.obj || []);
        else setCatalogError(msg?.msg || t('pages.index.managedCoreUnavailable'));
      })
      .catch(() => {
        if (current) setCatalogError(t('pages.index.managedCoreUnavailable'));
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [open, t]);

  function switchXrayVersion(version: string) {
    modal.confirm({
      title: t('pages.index.managedCoreConfirmTitle'),
      content: t('pages.index.managedCoreConfirm', { tag: version }),
      okText: t('confirm'),
      cancelText: t('cancel'),
      onOk: async () => {
        onClose();
        onBusy({ busy: true, tip: t('pages.index.dontRefresh') });
        try {
          await HttpUtil.post(`/panel/api/server/installXray/${encodeURIComponent(version)}`);
        } finally {
          onBusy({ busy: false });
        }
      },
    });
  }

  function updateGeofile(fileName: string) {
    const isSingle = !!fileName;
    modal.confirm({
      title: t('pages.index.geofileUpdateDialog'),
      content: isSingle
        ? t('pages.index.geofileUpdateDialogDesc').replace('#filename#', fileName)
        : t('pages.index.geofilesUpdateDialogDesc'),
      okText: t('confirm'),
      cancelText: t('cancel'),
      onOk: async () => {
        onClose();
        onBusy({ busy: true, tip: t('pages.index.dontRefresh') });
        const url = isSingle
          ? `/panel/api/server/updateGeofile/${fileName}`
          : '/panel/api/server/updateGeofile';
        try {
          await HttpUtil.post(url);
        } finally {
          onBusy({ busy: false });
        }
      },
    });
  }

  const activeKeyStr = Array.isArray(activeKey) ? activeKey[0] : activeKey;

  return (
    <Modal open={open} title={t('pages.index.xrayUpdates')} footer={null} onCancel={onClose}>
      {modalContextHolder}
      <Spin spinning={loading}>
        <Collapse
          accordion
          activeKey={activeKey}
          onChange={setActiveKey}
          items={[
            {
              key: '1',
              label: t('pages.index.managedCoreReleases'),
              children: (
                <>
                  <Alert
                    type="warning"
                    className="mb-12"
                    title={t('pages.index.managedCoreNotice')}
                    showIcon
                  />
                  <p>
                    {t(
                      status?.xray?.state === 'running'
                        ? 'pages.index.managedCoreRunning'
                        : 'pages.index.managedCoreStoppedVersion',
                      { version: status?.xray?.version || '-' },
                    )}
                  </p>
                  {catalogError && <Alert type="error" title={catalogError} showIcon />}
                  {!loading && !catalogError && versions.length === 0 && (
                    <p>{t('pages.index.managedCoreEmpty')}</p>
                  )}
                  <div className="version-list">
                    {versions.map((version, index) => (
                      <div key={version.tag} className="version-list-item">
                        <span>
                          <Tag color={index % 2 === 0 ? 'purple' : 'green'}>{version.tag}</Tag>
                          {version.prerelease && (
                            <Tag color="orange">{t('pages.index.managedCorePrerelease')}</Tag>
                          )}
                        </span>
                        <Button onClick={() => switchXrayVersion(version.tag)}>
                          {t('pages.index.managedCoreInstall')}
                        </Button>
                      </div>
                    ))}
                  </div>
                </>
              ),
            },
            {
              key: '2',
              label: 'Geofiles',
              children: (
                <>
                  <div className="version-list">
                    {GEOFILES.map((file, index) => (
                      <div key={file} className="version-list-item">
                        <Tag color={index % 2 === 0 ? 'purple' : 'green'}>{file}</Tag>
                        <Tooltip title={t('update')}>
                          <ReloadOutlined
                            className="reload-icon"
                            role="button"
                            tabIndex={0}
                            aria-label={t('update')}
                            onClick={() => updateGeofile(file)}
                            onKeyDown={activateOnKey(() => updateGeofile(file))}
                          />
                        </Tooltip>
                      </div>
                    ))}
                  </div>
                  <div className="actions-row">
                    <Button onClick={() => updateGeofile('')}>
                      {t('pages.index.geofilesUpdateAll')}
                    </Button>
                  </div>
                </>
              ),
            },
            {
              key: '3',
              label: t('pages.index.geodataTitle'),
              children: (
                <GeodataSection active={activeKeyStr === '3'} onBusy={onBusy} onClose={onClose} />
              ),
            },
          ]}
        />
      </Spin>
    </Modal>
  );
}
