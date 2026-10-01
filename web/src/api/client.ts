import type { Envelope, EnvelopeScope, Page, Principal } from './types';

export class ApiClientError extends Error {
  readonly code: string;
  readonly status: number;
  readonly retryable: boolean;

  constructor(message: string, options: { code: string; status: number; retryable: boolean }) {
    super(message);
    this.name = 'ApiClientError';
    this.code = options.code;
    this.status = options.status;
    this.retryable = options.retryable;
  }
}

export interface RequestEnvelopeInput {
  principal: Principal;
  scope: Omit<EnvelopeScope, 'tenant_id'>;
  payload?: unknown;
  memoryHint?: Envelope['memory_hint'];
  budget?: Envelope['budget'];
  privacy?: Envelope['privacy'];
  idempotencyKey?: string;
  requestId?: string;
}

export interface ApiClientOptions {
  baseUrl?: string;
  tenantId: string;
  fetchImpl?: typeof fetch;
  cacheTtlMs?: number;
  cacheLimit?: number;
  now?: () => number;
  newId?: () => string;
}

interface CacheEntry {
  value: unknown;
  expiresAt: number;
}

const DEFAULT_TTL_MS = 30_000;
const DEFAULT_LIMIT = 64;

// ApiClient is tenant-scoped: every Envelope is built from the active tenant,
// and a tenant switch clears the cache so no response can leak across tenants.
export class ApiClient {
  private readonly baseUrl: string;
  private readonly fetchImpl: typeof fetch;
  private readonly cacheTtlMs: number;
  private readonly cacheLimit: number;
  private readonly now: () => number;
  private readonly newId: () => string;
  private readonly cache = new Map<string, CacheEntry>();
  private tenantId: string;

  constructor(options: ApiClientOptions) {
    this.baseUrl = options.baseUrl ?? '';
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch.bind(globalThis);
    this.cacheTtlMs = options.cacheTtlMs ?? DEFAULT_TTL_MS;
    this.cacheLimit = options.cacheLimit ?? DEFAULT_LIMIT;
    this.now = options.now ?? (() => Date.now());
    this.newId = options.newId ?? (() => globalThis.crypto.randomUUID());
    this.tenantId = options.tenantId;
  }

  get activeTenantId(): string {
    return this.tenantId;
  }

  setActiveTenant(tenantId: string): void {
    if (tenantId === this.tenantId) {
      return;
    }
    this.tenantId = tenantId;
    this.clearCache();
  }

  clearCache(): void {
    this.cache.clear();
  }

  cacheSize(): number {
    return this.cache.size;
  }

  buildEnvelope<T>(input: RequestEnvelopeInput, payload: T): Envelope<T> {
    return {
      version: 'v1',
      request_id: input.requestId ?? this.newId(),
      idempotency_key: input.idempotencyKey ?? this.newId(),
      principal: input.principal,
      scope: { ...input.scope, tenant_id: this.tenantId },
      memory_hint: input.memoryHint,
      budget: input.budget,
      privacy: input.privacy ?? { visibility: 'private' },
      payload,
    };
  }

  async request<T>(path: string, input: RequestEnvelopeInput, payload?: unknown, method: 'POST' | 'PUT' = 'POST'): Promise<T> {
    const envelope = this.buildEnvelope(input, payload ?? {});
    let response: Response;
    try {
      response = await this.fetchImpl(`${this.baseUrl}${path}`, {
        method,
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(envelope),
      });
    } catch (error) {
      throw new ApiClientError(error instanceof Error ? error.message : 'network request failed', {
        code: 'NETWORK_ERROR',
        status: 0,
        retryable: true,
      });
    }
    let body: { data?: T; error?: { code: string; message: string; retryable: boolean } };
    try {
      body = (await response.json()) as typeof body;
    } catch {
      throw new ApiClientError('response was not valid JSON', { code: 'INVALID_RESPONSE', status: response.status, retryable: response.status >= 500 });
    }
    if (!response.ok || body.error) {
      const apiError = body.error ?? { code: 'HTTP_ERROR', message: `request failed with status ${response.status}`, retryable: response.status >= 500 };
      throw new ApiClientError(apiError.message, { code: apiError.code, status: response.status, retryable: apiError.retryable });
    }
    return body.data as T;
  }

  async cached<T>(cacheKey: string, loader: () => Promise<T>): Promise<T> {
    const entry = this.cache.get(cacheKey);
    const timestamp = this.now();
    if (entry && entry.expiresAt > timestamp) {
      return entry.value as T;
    }
    const value = await loader();
    this.cache.set(cacheKey, { value, expiresAt: timestamp + this.cacheTtlMs });
    while (this.cache.size > this.cacheLimit) {
      const oldest = this.cache.keys().next().value;
      if (oldest === undefined) {
        break;
      }
      this.cache.delete(oldest);
    }
    return value;
  }

  async list<T>(path: string, input: RequestEnvelopeInput, page = 1, pageSize = 20): Promise<Page<T>> {
    return this.request<Page<T>>(path, input, { page, page_size: pageSize });
  }
}

export type Permission = 'tenant.manage' | 'member.manage' | 'agent.manage' | 'memory.review' | 'memory.read' | 'evaluation.read';

const ROLE_PERMISSIONS: Record<string, Permission[]> = {
  owner: ['tenant.manage', 'member.manage', 'agent.manage', 'memory.review', 'memory.read', 'evaluation.read'],
  'tenant-admin': ['member.manage', 'agent.manage', 'memory.review', 'memory.read', 'evaluation.read'],
  member: ['memory.review', 'memory.read'],
};

export function can(role: string, permission: Permission): boolean {
  return (ROLE_PERMISSIONS[role] ?? []).includes(permission);
}

export function describeError(error: unknown): { code: string; message: string; retryable: boolean } {
  if (error instanceof ApiClientError) {
    return { code: error.code, message: error.message, retryable: error.retryable };
  }
  if (error instanceof Error) {
    return { code: 'UNKNOWN', message: error.message, retryable: false };
  }
  return { code: 'UNKNOWN', message: 'unknown error', retryable: false };
}
