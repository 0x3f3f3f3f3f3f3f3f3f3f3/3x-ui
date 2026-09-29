import { z } from 'zod';
import { PortSchema } from './primitives/port';
import { MieruCredentialsSchema } from './protocols/inbound/mieru';

export const MieruHostSchema = z.union([z.ipv4(), z.ipv6(), z.hostname()]);
export const MieruProfileSchema = z.object({
  profileName: z.string().min(1),
  user: z.object({
    name: MieruCredentialsSchema.shape.email,
    password: MieruCredentialsSchema.shape.password,
  }),
  servers: z
    .array(
      z
        .object({
          ipAddress: z.union([z.ipv4(), z.ipv6()]).optional(),
          domainName: z.hostname().optional(),
          portBindings: z
            .array(z.object({ port: PortSchema, protocol: z.enum(['TCP', 'UDP']) }))
            .min(1),
        })
        .refine((server) => !!server.ipAddress !== !!server.domainName),
    )
    .min(1),
});
export type MieruProfile = z.infer<typeof MieruProfileSchema>;
