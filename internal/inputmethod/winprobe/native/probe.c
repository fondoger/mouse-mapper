#include "protocol.h"
#include <bcrypt.h>

typedef BOOL (WINAPI *MM_IS_WOW64_PROCESS2)(HANDLE, USHORT *, USHORT *);
typedef BOOL (WINAPI *MM_GET_PROCESS_INFORMATION)(HANDLE, PROCESS_INFORMATION_CLASS, LPVOID, DWORD);
typedef NTSTATUS (WINAPI *MM_GEN_RANDOM)(BCRYPT_ALG_HANDLE, PUCHAR, ULONG, ULONG);

_Static_assert(sizeof(PROCESS_MACHINE_INFORMATION) == 8, "process architecture information size");

static WCHAR mm_dll_path[32768];
static MM_RESULT mm_output;

static BOOL mm_space(WCHAR value) {
    return value == L' ' || value == L'\t';
}

static const WCHAR *mm_skip_space(const WCHAR *input) {
    while (mm_space(*input)) ++input;
    return input;
}

static BOOL mm_decimal(const WCHAR **cursor, DWORD *output) {
    const WCHAR *input = mm_skip_space(*cursor);
    DWORD value = 0;
    DWORD digit;
    BOOL found = FALSE;
    while (*input != 0 && !mm_space(*input)) {
        if (*input < L'0' || *input > L'9') return FALSE;
        digit = (DWORD)(*input - L'0');
        if (value > 429496729UL || (value == 429496729UL && digit > 5)) return FALSE;
        value = value * 10 + digit;
        found = TRUE;
        ++input;
    }
    if (!found) return FALSE;
    *output = value;
    *cursor = input;
    return TRUE;
}

static BOOL mm_hexadecimal(const WCHAR **cursor, ULONGLONG *output) {
    const WCHAR *input = mm_skip_space(*cursor);
    ULONGLONG value = 0;
    DWORD digit;
    BOOL found = FALSE;
    while (*input != 0 && !mm_space(*input)) {
        if (*input >= L'0' && *input <= L'9') digit = (DWORD)(*input - L'0');
        else if (*input >= L'a' && *input <= L'f') digit = (DWORD)(*input - L'a') + 10;
        else if (*input >= L'A' && *input <= L'F') digit = (DWORD)(*input - L'A') + 10;
        else return FALSE;
        if ((value >> 60) != 0) return FALSE;
        value = (value << 4) | digit;
        found = TRUE;
        ++input;
    }
    if (!found || value == 0 || value != (ULONGLONG)(UINT_PTR)value) return FALSE;
    *output = value;
    *cursor = input;
    return TRUE;
}

static BOOL mm_arguments(MM_REQUEST *request, DWORD *timeout) {
    const WCHAR *input = mm_skip_space(GetCommandLineW());
    BOOL quoted = FALSE;
    while (*input != 0) {
        if (*input == L'"') quoted = !quoted;
        else if (!quoted && mm_space(*input)) break;
        ++input;
    }
    if (quoted || !mm_decimal(&input, &request->process) || request->process == 0 ||
        !mm_decimal(&input, &request->thread) || request->thread == 0 ||
        !mm_hexadecimal(&input, &request->window) ||
        !mm_decimal(&input, timeout) || *timeout == 0 || *timeout > MM_MAX_TIMEOUT ||
        *mm_skip_space(input) != 0) return FALSE;
    return TRUE;
}

static BOOL mm_target_matches(const MM_REQUEST *request, HANDLE process) {
    DWORD window_process = 0;
    DWORD thread = GetWindowThreadProcessId((HWND)(UINT_PTR)request->window, &window_process);
    return thread == request->thread && window_process == request->process &&
        WaitForSingleObject(process, 0) == WAIT_TIMEOUT;
}

static BOOL mm_same_architecture(HANDLE target) {
    HMODULE kernel = GetModuleHandleW(L"kernel32.dll");
    MM_IS_WOW64_PROCESS2 query;
    MM_GET_PROCESS_INFORMATION machine_query;
    PROCESS_MACHINE_INFORMATION information;
    DWORD error;
    USHORT process_machine = 0, native_machine = 0;
    if (kernel == NULL) return FALSE;

    /* x64 emulation on ARM64 is not WOW64. Ask for the actual process ISA first. */
    machine_query = (MM_GET_PROCESS_INFORMATION)(UINT_PTR)GetProcAddress(kernel, "GetProcessInformation");
    if (machine_query != NULL) {
        mm_zero(&information, sizeof(information));
        if (machine_query(target, ProcessMachineTypeInfo, &information, sizeof(information))) {
            if (information.ProcessMachine == MM_MACHINE) return TRUE;
            SetLastError(ERROR_NOT_SUPPORTED);
            return FALSE;
        }
        error = GetLastError();
        if (error != ERROR_INVALID_PARAMETER && error != ERROR_NOT_SUPPORTED &&
            error != ERROR_INVALID_FUNCTION && error != ERROR_CALL_NOT_IMPLEMENTED) return FALSE;
    } else if (GetLastError() != ERROR_PROC_NOT_FOUND) {
        return FALSE;
    }

    /* Older Windows may lack this information class; never infer a 64-bit ARM ISA. */
    query = (MM_IS_WOW64_PROCESS2)(UINT_PTR)GetProcAddress(kernel, "IsWow64Process2");
    if (query == NULL || !query(target, &process_machine, &native_machine)) return FALSE;
    if (process_machine == IMAGE_FILE_MACHINE_UNKNOWN) {
        if (native_machine == IMAGE_FILE_MACHINE_ARM64) {
            SetLastError(ERROR_NOT_SUPPORTED);
            return FALSE;
        }
        process_machine = native_machine;
    }
    if (process_machine == MM_MACHINE) return TRUE;
    SetLastError(ERROR_NOT_SUPPORTED);
    return FALSE;
}

static HMODULE mm_load_hook(void) {
    const WCHAR *basename = MM_DLL_NAME;
    DWORD length = GetModuleFileNameW(NULL, mm_dll_path, (DWORD)(sizeof(mm_dll_path) / sizeof(WCHAR)));
    DWORD i, directory = 0;
    if (length == 0 || length >= (sizeof(mm_dll_path) / sizeof(WCHAR))) return NULL;
    for (i = 0; i != length; ++i) {
        if (mm_dll_path[i] == L'\\' || mm_dll_path[i] == L'/') directory = i + 1;
    }
    if (directory == 0 || directory + (sizeof(MM_DLL_NAME) / sizeof(WCHAR)) >
        (sizeof(mm_dll_path) / sizeof(WCHAR))) {
        SetLastError(ERROR_BAD_PATHNAME);
        return NULL;
    }
    for (i = 0; i != (sizeof(MM_DLL_NAME) / sizeof(WCHAR)); ++i) mm_dll_path[directory + i] = basename[i];
    return LoadLibraryExW(mm_dll_path, NULL,
                          LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32);
}

static BOOL mm_random_nonce(ULONGLONG *nonce) {
    HMODULE module = LoadLibraryExW(L"bcrypt.dll", NULL, LOAD_LIBRARY_SEARCH_SYSTEM32);
    MM_GEN_RANDOM generate;
    NTSTATUS status;
    if (module == NULL) {
        mm_output.winerr = GetLastError();
        return FALSE;
    }
    generate = (MM_GEN_RANDOM)(UINT_PTR)GetProcAddress(module, "BCryptGenRandom");
    if (generate == NULL) {
        mm_output.winerr = GetLastError();
        FreeLibrary(module);
        return FALSE;
    }
    status = generate(NULL, (PUCHAR)nonce, sizeof(*nonce), BCRYPT_USE_SYSTEM_PREFERRED_RNG);
    FreeLibrary(module);
    if (status != 0 || *nonce == 0) {
        mm_output.hresult = status;
        return FALSE;
    }
    return TRUE;
}

static void mm_run(MM_REQUEST *request, DWORD timeout) {
    HANDLE process = NULL, mapping = NULL;
    HMODULE module = NULL;
    HHOOK hook = NULL;
    MM_SHARED *shared = NULL;
    HOOKPROC callback;
    WCHAR name[80];
    DWORD error, remaining;
    DWORD_PTR response = 0;
    ULONGLONG now;
    UINT message;
    LRESULT sent;

    request->magic = MM_REQUEST_MAGIC;
    request->version = MM_VERSION;
    request->size = sizeof(MM_SHARED);
    request->helper_process = GetCurrentProcessId();
    request->deadline = GetTickCount64() + timeout;
    process = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION | SYNCHRONIZE, FALSE, request->process);
    mm_output.status = MM_TARGET_CHANGED;
    if (process == NULL) {
        mm_output.winerr = GetLastError();
        goto done;
    }
    if (request->process == request->helper_process || !mm_target_matches(request, process)) goto done;
    mm_output.status = MM_UNSUPPORTED;
    if (!mm_same_architecture(process)) {
        mm_output.winerr = GetLastError();
        goto done;
    }

    mm_output.status = MM_RANDOM_FAILED;
    if (!mm_random_nonce(&request->nonce)) goto done;
    mm_mapping_name(name, request->nonce);
    mm_output.status = MM_MAPPING_FAILED;
    SetLastError(ERROR_SUCCESS);
    mapping = CreateFileMappingW(INVALID_HANDLE_VALUE, NULL, PAGE_READWRITE, 0, sizeof(MM_SHARED), name);
    error = GetLastError();
    if (mapping == NULL || error == ERROR_ALREADY_EXISTS) {
        mm_output.winerr = error;
        goto done;
    }
    shared = (MM_SHARED *)MapViewOfFile(mapping, FILE_MAP_READ | FILE_MAP_WRITE, 0, 0, sizeof(MM_SHARED));
    if (shared == NULL) {
        mm_output.winerr = GetLastError();
        goto done;
    }
    mm_zero(shared, sizeof(*shared));
    mm_copy(&shared->request, request, sizeof(*request));

    mm_output.status = MM_HOOK_LOAD_FAILED;
    module = mm_load_hook();
    if (module == NULL) {
        mm_output.winerr = GetLastError();
        goto done;
    }
    callback = (HOOKPROC)(UINT_PTR)GetProcAddress(module, "MouseMapperProbeHook");
    if (callback == NULL) {
        mm_output.winerr = GetLastError();
        goto done;
    }
    message = RegisterWindowMessageW(MM_MESSAGE_NAME);
    if (message == 0) {
        mm_output.winerr = GetLastError();
        goto done;
    }
    mm_output.status = MM_TARGET_CHANGED;
    if (!mm_target_matches(request, process)) goto done;
    mm_output.status = MM_HOOK_INSTALL_FAILED;
    hook = SetWindowsHookExW(WH_CALLWNDPROC, callback, module, request->thread);
    if (hook == NULL) {
        mm_output.winerr = GetLastError();
        goto done;
    }
    now = GetTickCount64();
    mm_output.status = MM_EXPIRED;
    if (now >= request->deadline) goto done;
    remaining = (DWORD)(request->deadline - now);
    mm_output.status = MM_SEND_FAILED;
    SetLastError(ERROR_SUCCESS);
    sent = SendMessageTimeoutW((HWND)(UINT_PTR)request->window, message,
                              (WPARAM)(DWORD)request->nonce,
                              (LPARAM)(UINT_PTR)(DWORD)(request->nonce >> 32),
                              SMTO_BLOCK | SMTO_ABORTIFHUNG | SMTO_ERRORONEXIT,
                              remaining, &response);
    if (sent == 0) {
        error = GetLastError();
        mm_output.winerr = error == ERROR_SUCCESS ? ERROR_TIMEOUT : error;
        goto done;
    }
    mm_output.status = MM_EXPIRED;
    if (GetTickCount64() >= request->deadline) goto done;
    mm_output.status = MM_TARGET_CHANGED;
    if (!mm_target_matches(request, process)) goto done;
    mm_output.status = MM_NO_RESPONSE;
    if (InterlockedCompareExchange(&shared->state, MM_COMPLETE, MM_COMPLETE) != MM_COMPLETE) goto done;
    mm_output.status = MM_PROTOCOL_ERROR;
    if (!mm_equal(&shared->request, request, sizeof(*request)) || shared->reserved != 0 ||
        shared->result.magic != MM_RESULT_MAGIC || shared->result.version != MM_VERSION ||
        shared->result.size != sizeof(MM_RESULT) || shared->result.machine != MM_MACHINE ||
        shared->result.process != request->process || shared->result.thread != request->thread ||
        shared->result.window != request->window || shared->result.status > MM_PROTOCOL_ERROR ||
        shared->result.reserved16 != 0 || shared->result.reserved64 != 0) goto done;
    mm_copy(&mm_output, &shared->result, sizeof(mm_output));

done:
    if (shared != NULL) InterlockedExchange(&shared->state, MM_CANCELLED);
    if (hook != NULL) UnhookWindowsHookEx(hook);
    if (module != NULL) FreeLibrary(module);
    if (shared != NULL) UnmapViewOfFile(shared);
    if (mapping != NULL) CloseHandle(mapping);
    if (process != NULL) CloseHandle(process);
}

/* The linker uses this cdecl symbol directly; there is no CRT startup. */
void mm_probe_entry(void) {
    MM_REQUEST request;
    DWORD timeout = 0, written = 0;
    HANDLE output;
    mm_zero(&request, sizeof(request));
    mm_init_result(&mm_output, NULL);
    mm_output.status = MM_INVALID_ARGUMENTS;
    if (mm_arguments(&request, &timeout)) {
        mm_output.process = request.process;
        mm_output.thread = request.thread;
        mm_output.window = request.window;
        mm_run(&request, timeout);
    }
    output = GetStdHandle(STD_OUTPUT_HANDLE);
    if (output == INVALID_HANDLE_VALUE || output == NULL ||
        !WriteFile(output, &mm_output, sizeof(mm_output), &written, NULL) || written != sizeof(mm_output)) {
        ExitProcess(1);
    }
    ExitProcess(mm_output.status == MM_OK ? 0 : 1);
}
