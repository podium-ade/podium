import { Code, ConnectError, createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { NodeAdminService } from "../gen/podium/v1/admin_pb";
import { ArtifactService } from "../gen/podium/v1/artifact_pb";
import { IdentityService } from "../gen/podium/v1/identity_pb";
import { SecretService } from "../gen/podium/v1/secret_pb";
import { TaskService } from "../gen/podium/v1/task_pb";
import { getToken, notifyRejected } from "./auth";

// The tailnet transport wants no header at all — identity comes from the connection — so the
// interceptor only adds one when a dev token has actually been entered.
const bearer: Interceptor = (next) => async (req) => {
  const token = getToken();
  if (token) req.header.set("Authorization", `Bearer ${token}`);
  try {
    return await next(req);
  } catch (err) {
    if (isUnauthenticated(err)) notifyRejected();
    throw err;
  }
};

export function isUnauthenticated(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.Unauthenticated;
}

export function errorMessage(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage;
  if (err instanceof Error) return err.message;
  return String(err);
}

const transport = createConnectTransport({
  baseUrl: "/",
  interceptors: [bearer],
});

export const tasks = createClient(TaskService, transport);
export const admin = createClient(NodeAdminService, transport);
export const identity = createClient(IdentityService, transport);
export const secrets = createClient(SecretService, transport);
export const artifacts = createClient(ArtifactService, transport);

/** connectCode exposes the Connect status code so a screen can react to one by name. */
export function connectCode(err: unknown): Code | undefined {
  return err instanceof ConnectError ? err.code : undefined;
}

export { Code };
