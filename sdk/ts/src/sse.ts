export interface SseMessage {
  id: string | undefined;
  event: string;
  data: string;
  retry: number | undefined;
}

const lineEnd = /\r\n|\n|\r/;

export async function* parseSse(body: ReadableStream<Uint8Array>): AsyncGenerator<SseMessage> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let data: string[] = [];
  let event = "";
  let id: string | undefined;
  let retry: number | undefined;
  let done = false;

  const line = function* (text: string): Generator<SseMessage> {
    if (text === "") {
      if (data.length > 0) {
        yield { id, event: event || "message", data: data.join("\n"), retry };
      }
      data = [];
      event = "";
      retry = undefined;
      return;
    }
    if (text.startsWith(":")) return;
    const at = text.indexOf(":");
    const field = at === -1 ? text : text.slice(0, at);
    let value = at === -1 ? "" : text.slice(at + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "data":
        data.push(value);
        break;
      case "event":
        event = value;
        break;
      case "id":
        if (!value.includes("\0")) id = value;
        break;
      case "retry":
        if (/^\d+$/.test(value)) retry = Number(value);
        break;
    }
  };

  try {
    while (!done) {
      const chunk = await reader.read();
      done = chunk.done;
      buffer += chunk.done ? decoder.decode() : decoder.decode(chunk.value, { stream: true });
      for (;;) {
        const m = lineEnd.exec(buffer);
        if (m === null) break;
        if (m[0] === "\r" && m.index + 1 === buffer.length && !done) break;
        const text = buffer.slice(0, m.index);
        buffer = buffer.slice(m.index + m[0].length);
        yield* line(text);
      }
    }
  } finally {
    await reader.cancel().catch(() => undefined);
  }
}
