import { z } from 'zod';

export const PasswordProxyAccountSchema = z.object({
  user: z.string().min(1),
  pass: z.string().min(1),
  ownerClientId: z.uuid().optional(),
});

export function validatePasswordAccountOwners(
  accounts: z.infer<typeof PasswordProxyAccountSchema>[] | undefined,
  ctx: z.RefinementCtx,
) {
  if (!accounts?.some((account) => account.ownerClientId)) return;
  accounts.forEach((account, index) => {
    if (!account.ownerClientId) {
      ctx.addIssue({
        code: 'custom',
        path: ['accounts', index, 'ownerClientId'],
        message: 'pages.inbounds.form.ownerClientRequired',
      });
    }
  });
}
