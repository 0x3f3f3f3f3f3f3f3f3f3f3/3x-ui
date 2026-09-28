import type { ClientRecord, InboundOption } from '@/schemas/client';
import { SSHPublicKeySchema, SSHTargetSchema } from '@/schemas/ssh';
import { preferPublicHost, resolveShareHost } from '@/lib/xray/inbound-link';

export function buildSSHClientExport(
  client: ClientRecord,
  inbound: InboundOption,
  host: string,
  publicHost = '',
) {
  if (
    inbound.shareAddrStrategy === 'custom' &&
    (!SSHTargetSchema.shape.host.safeParse(inbound.shareAddr?.replace(/^\[|\]$/g, '')).success ||
      inbound.shareAddr?.trim() === '*')
  )
    return null;
  const endpoint = resolveShareHost(inbound, '', preferPublicHost(host, publicHost)).replace(
    /^\[|\]$/g,
    '',
  );
  const key = SSHPublicKeySchema.safeParse(inbound.sshHostKey);
  const target = SSHTargetSchema.safeParse({ host: endpoint, port: inbound.port });
  if (
    !key.success ||
    !target.success ||
    endpoint === '*' ||
    !inbound.port ||
    inbound.protocol !== 'ssh' ||
    inbound.nodeId ||
    !Number.isSafeInteger(inbound.id) ||
    inbound.id <= 0 ||
    !client.email ||
    client.email.length > 64 ||
    /[\s"\\#$%]/u.test(client.email) ||
    Array.from(client.email).some((c) => c.charCodeAt(0) < 0x20 || c.charCodeAt(0) === 0x7f)
  )
    return null;
  const alias = `xui-ssh-${inbound.id}`;
  const configName = `${alias}.conf`;
  const knownHostsName = `${alias}.known_hosts`;
  const knownHost = inbound.port === 22 ? endpoint : `[${endpoint}]:${inbound.port}`;
  const publicKey = key.data
    .split(/[ \t]+/)
    .slice(0, 2)
    .join(' ');
  const config = [
    `Host ${alias}`,
    `  HostName ${endpoint}`,
    `  Port ${inbound.port}`,
    `  User ${client.email}`,
    '  IdentityFile none',
    '  IdentitiesOnly yes',
    '  PreferredAuthentications publickey',
    '  StrictHostKeyChecking yes',
    `  UserKnownHostsFile ./${knownHostsName}`,
    '  GlobalKnownHostsFile none',
    '  KnownHostsCommand none',
    '  VerifyHostKeyDNS no',
    '  UpdateHostKeys no',
    '  SessionType none',
    '  RequestTTY no',
    '  ForwardAgent no',
    '  ForwardX11 no',
    '  ExitOnForwardFailure yes',
    '',
  ].join('\n');
  return { alias, configName, knownHostsName, config, knownHosts: `${knownHost} ${publicKey}\n` };
}
