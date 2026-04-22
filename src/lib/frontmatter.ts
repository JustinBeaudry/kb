import { parse as yamlParse, stringify as yamlStringify } from "yaml";

export interface FrontmatterResult<T = Record<string, unknown>> {
  data: T;
  body: string;
}

const DELIMITER = "---";

export function parseFrontmatter<T = Record<string, unknown>>(
  content: string
): FrontmatterResult<T> {
  if (!content.startsWith(`${DELIMITER}\n`) && !content.startsWith(`${DELIMITER}\r\n`)) {
    return { data: {} as T, body: content };
  }

  const afterOpener = content.slice(DELIMITER.length).replace(/^\r?\n/, "");
  const closerMatch = afterOpener.match(/\r?\n---\r?\n?/);
  if (!closerMatch || closerMatch.index === undefined) {
    return { data: {} as T, body: content };
  }

  const yamlText = afterOpener.slice(0, closerMatch.index);
  const bodyStart = closerMatch.index + closerMatch[0].length;
  let body = afterOpener.slice(bodyStart);
  if (body.startsWith("\n")) body = body.slice(1);

  let data: T;
  try {
    data = (yamlParse(yamlText) ?? {}) as T;
  } catch (cause) {
    const message = cause instanceof Error ? cause.message : String(cause);
    throw new Error(`Invalid YAML frontmatter: ${message}`);
  }

  return { data, body };
}

export function serializeFrontmatter(
  data: Record<string, unknown>,
  body: string
): string {
  if (Object.keys(data).length === 0) return body;
  const yamlText = yamlStringify(data).trimEnd();
  const bodyPart = body.length === 0 ? "" : `\n${body}`;
  return `${DELIMITER}\n${yamlText}\n${DELIMITER}\n${bodyPart}`;
}
