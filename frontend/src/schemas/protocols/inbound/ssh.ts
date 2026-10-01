import { z } from 'zod';
import { SSHAuthorizedKeysSchema, SSHPasswordSchema, SSHUsernameSchema } from '../shared/ssh';

export const SSHClientSchema = z
  .object({
    email: z.string().min(1),
    sshUsername: SSHUsernameSchema.default(''),
    sshAuthorizedKeys: SSHAuthorizedKeysSchema.default(''),
    sshPassword: SSHPasswordSchema.default(''),
    clearSshPassword: z.boolean().optional(),
    clearSshAuthorizedKeys: z.boolean().optional(),
    enable: z.boolean().default(true),
  })
  .loose();
const bounded = (max: number, fallback: number) =>
  z.number().int().min(0).max(max).default(fallback);
export const SSHReverseFieldsSchema = z
  .object({
    enabled: z.boolean().default(false),
    bindAddresses: z.array(z.ipv4().or(z.ipv6())).max(16).default([]),
    portFrom: bounded(65535, 1),
    portTo: bounded(65535, 65535),
    sourceCIDRs: z.array(z.cidrv4().or(z.cidrv6())).max(64).default([]),
    maxListeners: bounded(16, 4),
    allowPortZero: z.boolean().default(false),
  })
  .strict();
export const SSHReverseSchema = SSHReverseFieldsSchema.superRefine((value, ctx) => {
  if (!value.enabled) return;
  if (
    !value.bindAddresses.length ||
    !value.sourceCIDRs.length ||
    !value.portFrom ||
    value.portTo < value.portFrom
  )
    ctx.addIssue({ code: 'custom', message: 'pages.inbounds.form.ssh.reverseInvalid' });
});
export const SSHInboundSettingsSchema = z
  .object({
    clients: z.array(SSHClientSchema).default([]),
    allowPassword: z.boolean().default(false),
    handshakeTimeoutSeconds: bounded(120, 10),
    channelOpenTimeoutSeconds: bounded(120, 10),
    idleTimeoutSeconds: bounded(86400, 300),
    maxAuthTries: bounded(16, 6),
    maxConnections: bounded(1024, 64),
    maxConnectionsPerUser: bounded(64, 4),
    maxChannelsPerConnection: bounded(64, 16),
    maxChannels: bounded(512, 128),
    reverse: SSHReverseSchema.default(SSHReverseSchema.parse({})),
  })
  .strict();
export type SSHInboundSettings = z.infer<typeof SSHInboundSettingsSchema>;
