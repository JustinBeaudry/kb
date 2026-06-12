const encoder = new TextEncoder();

export function byteLength(s: string): number {
  return encoder.encode(s).length;
}
