import {
  ClientTrafficResetRequestSchema,
  type ClientTrafficResetRequest,
  ClientTrafficBatchResetRequestSchema,
  type ClientTrafficBatchResetRequest,
} from '@/schemas/client';
import { RandomUtil } from '@/utils';
import { withBasePath } from '@/api/http-init';

const storageKey = (clientId: string) => `client-traffic-reset:${clientId}`;

// Keep an uncertain reset across page reloads; only a successful reply releases its key.
export function pendingClientTrafficReset(clientId: string): ClientTrafficResetRequest {
  const key = storageKey(clientId);
  const request = ClientTrafficResetRequestSchema.parse({
    clientId,
    requestId: sessionStorage.getItem(key) ?? RandomUtil.randomUUID(),
  });
  sessionStorage.setItem(key, request.requestId);
  return request;
}

export function acknowledgeClientTrafficReset(request: ClientTrafficResetRequest) {
  const key = storageKey(request.clientId);
  if (sessionStorage.getItem(key) === request.requestId) sessionStorage.removeItem(key);
}

const allClientsStorageKey = () => `client-traffic-reset-all:${withBasePath('/panel/api/clients')}`;

export function pendingAllClientTrafficReset(): ClientTrafficBatchResetRequest {
  const key = allClientsStorageKey();
  const request = ClientTrafficBatchResetRequestSchema.parse({
    requestId: sessionStorage.getItem(key) ?? RandomUtil.randomUUID(),
  });
  sessionStorage.setItem(key, request.requestId);
  return request;
}

export function acknowledgeAllClientTrafficReset(request: ClientTrafficBatchResetRequest) {
  const key = allClientsStorageKey();
  if (sessionStorage.getItem(key) === request.requestId) {
    sessionStorage.removeItem(key);
  }
}
