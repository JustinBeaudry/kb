/* A fixed-arity boundary is required: macOS arm64 passes variadic arguments
 * on the stack, so binding libc openat directly as a four-argument FFI function
 * silently corrupts the creation mode. The C compiler owns that ABI detail. */
extern int openat(int directory, const char *path, int flags, ...);
int kb_openat(int directory, const char *path, int flags, int mode) {
  return openat(directory, path, flags, mode);
}
