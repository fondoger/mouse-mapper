#ifndef MOUSE_MAPPER_IME_PROTOCOL_H
#define MOUSE_MAPPER_IME_PROTOCOL_H

#ifndef _WIN32_WINNT
#define _WIN32_WINNT 0x0A00
#endif
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <stddef.h>

#define MM_RESULT_MAGIC 0x50494d4dUL
#define MM_REQUEST_MAGIC 0x51494d4dUL
#define MM_VERSION 1
#define MM_MAX_TIMEOUT 1500
#define MM_MESSAGE_NAME L"MouseMapper.InputMethod.ReadExistingThread.v1"
#define MM_MAPPING_PREFIX L"Local\\MouseMapper.IME.v1."

#if defined(_M_ARM64) || defined(__aarch64__)
#define MM_MACHINE 0xaa64UL
#define MM_DEFAULT_DLL_NAME L"ime-hook-arm64.dll"
#elif defined(_M_X64) || defined(__x86_64__)
#define MM_MACHINE 0x8664UL
#define MM_DEFAULT_DLL_NAME L"ime-hook-amd64.dll"
#elif defined(_M_IX86) || defined(__i386__)
#define MM_MACHINE 0x014cUL
#define MM_DEFAULT_DLL_NAME L"ime-hook-386.dll"
#else
#error Unsupported Windows instruction set
#endif
#ifndef MM_DLL_NAME
#define MM_DLL_NAME MM_DEFAULT_DLL_NAME
#endif

enum MM_STATUS {
    MM_OK = 0,
    MM_INVALID_ARGUMENTS = 1,
    MM_TARGET_CHANGED = 2,
    MM_UNSUPPORTED = 3,
    MM_RANDOM_FAILED = 4,
    MM_MAPPING_FAILED = 5,
    MM_HOOK_LOAD_FAILED = 6,
    MM_HOOK_INSTALL_FAILED = 7,
    MM_SEND_FAILED = 8,
    MM_NO_RESPONSE = 9,
    MM_NO_EXISTING_TSF = 10,
    MM_PROFILE_MANAGER_FAILED = 11,
    MM_ACTIVE_PROFILE_FAILED = 12,
    MM_INVALID_PROFILE = 13,
    MM_EXPIRED = 14,
    MM_PROTOCOL_ERROR = 15
};

enum MM_STATE {
    MM_PENDING = 0,
    MM_CLAIMED = 1,
    MM_COMPLETE = 2,
    MM_CANCELLED = 3
};

/* Fixed-width, pointer-free, little-endian stdout protocol on every ABI. */
typedef struct MM_RESULT {
    DWORD magic;
    WORD version;
    WORD size;
    DWORD status;
    DWORD winerr;
    LONG hresult;
    DWORD process;
    DWORD thread;
    DWORD kind;
    WORD language;
    WORD reserved16;
    DWORD machine;
    ULONGLONG layout;
    GUID clsid;
    GUID profile;
    ULONGLONG window;
    ULONGLONG reserved64;
} MM_RESULT;

typedef struct MM_REQUEST {
    DWORD magic;
    WORD version;
    WORD size;
    DWORD helper_process;
    DWORD process;
    DWORD thread;
    DWORD reserved;
    ULONGLONG window;
    ULONGLONG nonce;
    ULONGLONG deadline;
} MM_REQUEST;

typedef struct MM_SHARED {
    MM_REQUEST request;
    volatile LONG state;
    DWORD reserved;
    MM_RESULT result;
} MM_SHARED;

_Static_assert(sizeof(MM_RESULT) == 96, "result protocol size");
_Static_assert(offsetof(MM_RESULT, layout) == 40, "result HKL offset");
_Static_assert(offsetof(MM_RESULT, clsid) == 48, "result CLSID offset");
_Static_assert(offsetof(MM_RESULT, profile) == 64, "result profile offset");
_Static_assert(offsetof(MM_RESULT, window) == 80, "result HWND offset");
_Static_assert(sizeof(MM_REQUEST) == 48, "request protocol size");
_Static_assert(offsetof(MM_SHARED, state) == 48, "aligned atomic state");
_Static_assert(sizeof(MM_SHARED) == 152, "mapping protocol size");

/* Volatile byte operations keep these freestanding binaries CRT-free. */
static inline void mm_zero(void *destination, SIZE_T count) {
    volatile BYTE *bytes = (volatile BYTE *)destination;
    while (count != 0) {
        *bytes++ = 0;
        --count;
    }
}

static inline void mm_copy(void *destination, const void *source, SIZE_T count) {
    volatile BYTE *out = (volatile BYTE *)destination;
    const volatile BYTE *in = (const volatile BYTE *)source;
    while (count != 0) {
        *out++ = *in++;
        --count;
    }
}

static inline BOOL mm_equal(const void *left, const void *right, SIZE_T count) {
    const volatile BYTE *a = (const volatile BYTE *)left;
    const volatile BYTE *b = (const volatile BYTE *)right;
    while (count != 0) {
        if (*a++ != *b++) return FALSE;
        --count;
    }
    return TRUE;
}

static inline void mm_mapping_name(WCHAR *output, ULONGLONG nonce) {
    const WCHAR *prefix = MM_MAPPING_PREFIX;
    const WCHAR *digits = L"0123456789abcdef";
    SIZE_T length = (sizeof(MM_MAPPING_PREFIX) / sizeof(WCHAR)) - 1;
    SIZE_T i;
    for (i = 0; i != length; ++i) output[i] = prefix[i];
    for (i = 0; i != 16; ++i) {
        output[length + 15 - i] = digits[(DWORD)(nonce & 15)];
        nonce >>= 4;
    }
    output[length + 16] = 0;
}

static inline void mm_init_result(MM_RESULT *result, const MM_REQUEST *request) {
    mm_zero(result, sizeof(*result));
    result->magic = MM_RESULT_MAGIC;
    result->version = MM_VERSION;
    result->size = sizeof(*result);
    result->machine = MM_MACHINE;
    if (request != NULL) {
        result->process = request->process;
        result->thread = request->thread;
        result->window = request->window;
    }
}

#endif
