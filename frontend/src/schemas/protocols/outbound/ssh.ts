import { z } from 'zod';
import { SSHHostKeySchema, SSHPasswordSchema, SSHUsernameSchema } from '../shared/ssh';

export const SSHOutboundSettingsSchema = z
  .object({
    address: z.string().min(1).max(253),
    port: z.number().int().min(1).max(65535).default(22),
    username: SSHUsernameSchema.refine((value) => value !== '', 'pages.clients.sshUsernameInvalid'),
    password: SSHPasswordSchema.default(''),
    privateKeyFile: z
      .string()
      .default('')
      .refine(
        (value) =>
          !value ||
          (/^\/[^\r\n\0]*\/native-ssh\/outbound\/[^/]+$/.test(value) &&
            !value.split('/').some((part) => part === '.' || part === '..')),
        'pages.xray.outboundForm.sshPrivateKeyPathInvalid',
      ),
    hostKey: SSHHostKeySchema,
    handshakeTimeoutSeconds: z.number().int().min(0).max(120).default(10),
    idleTimeoutSeconds: z.number().int().min(0).max(86400).default(300),
  })
  .strict()
  .refine((value) => !!value.password || !!value.privateKeyFile, {
    path: ['password'],
    message: 'pages.xray.outboundForm.sshAuthenticationRequired',
  });
export type SSHOutboundSettings = z.infer<typeof SSHOutboundSettingsSchema>;
