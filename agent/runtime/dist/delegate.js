// The delegation tools, as the conductor's TurnService sees them.
//
// This is the client half: four calls over Connect's JSON protocol, made with plain fetch.
// Connect's unary JSON mapping is deliberately boring — POST to
// `/<fully.qualified.Service>/<Method>`, a JSON body, a JSON body back, and a JSON error
// document with a `code` on failure — so a generated client would buy nothing here and cost
// a build step inside the runtime image.
//
// The token is a bearer for ONE turn: the conductor minted it for the turn this process is
// running, scoped to that turn's conversation and to the playbooks its brief listed, and it
// is revoked the moment the turn ends. It never appears in an argument or an error message.
const Service = "podium.agent.v1.TurnService";
/** TurnTokenHeader must match internal/agent/api.TurnTokenHeader. */
export const TurnTokenHeader = "X-Podium-Turn";
/** callTimeoutMs bounds one call. Delegate creates a task and returns; nothing here waits
 * for a task to finish, so a call that takes this long is a conductor in trouble. */
const callTimeoutMs = 30_000;
/** DelegateError is a refusal from the conductor, with the Connect code it came with. The
 * message is the conductor's own words, which a model can act on: "that playbook was not
 * offered to this turn" is correctable, "invalid argument" is not. */
export class DelegateError extends Error {
    code;
    constructor(code, message) {
        super(message);
        this.name = "DelegateError";
        this.code = code;
    }
}
/** Client is the conductor's turn API. */
export class Client {
    url;
    token;
    fetchImpl;
    constructor(url, token, fetchImpl = fetch) {
        this.url = url.replace(/\/+$/, "");
        this.token = token;
        this.fetchImpl = fetchImpl;
    }
    delegate(playbook, instruction) {
        return this.call("Delegate", { playbook, instruction });
    }
    get(id) {
        return this.call("GetDelegation", { id });
    }
    list() {
        return this.call("ListDelegations", {});
    }
    cancel(id, reason) {
        return this.call("CancelDelegation", { id, reason });
    }
    async call(method, body) {
        const res = await this.fetchImpl(`${this.url}/${Service}/${method}`, {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
                [TurnTokenHeader]: this.token,
            },
            body: JSON.stringify(body),
            signal: AbortSignal.timeout(callTimeoutMs),
        });
        const text = await res.text();
        if (!res.ok) {
            throw new DelegateError(...connectError(res.status, text));
        }
        if (text.trim() === "") {
            return {};
        }
        try {
            return JSON.parse(text);
        }
        catch (err) {
            throw new DelegateError("internal", `${method} answered with something that is not JSON: ${err}`);
        }
    }
}
/** connectError reads Connect's JSON error document, falling back to the status line for a
 * body that is not one — which is what a proxy in front of the conductor would return. */
function connectError(status, body) {
    try {
        const parsed = JSON.parse(body);
        if (parsed.code || parsed.message) {
            return [parsed.code ?? String(status), parsed.message ?? `HTTP ${status}`];
        }
    }
    catch {
        // Not a Connect error document.
    }
    const trimmed = body.trim();
    return [String(status), trimmed === "" ? `HTTP ${status}` : `HTTP ${status}: ${trimmed}`];
}
