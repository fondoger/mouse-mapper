#define CINTERFACE
#define COBJMACROS
#include "protocol.h"
#include <msctf.h>

static const GUID mm_profiles_clsid =
    {0x33c53a50, 0xf456, 0x4884, {0xb0, 0x49, 0x85, 0xfd, 0x64, 0x3e, 0xcf, 0xed}};
static const GUID mm_profile_manager_iid =
    {0x71c6e74c, 0x0f28, 0x11d8, {0xa8, 0x2a, 0x00, 0x06, 0x5b, 0x84, 0x43, 0x5c}};
static const GUID mm_keyboard_category =
    {0x34745c63, 0xb2f0, 0x4784, {0x8b, 0x67, 0x5e, 0x12, 0xc8, 0x70, 0x1a, 0x31}};
static const GUID mm_null_guid = {0};

typedef HRESULT (WINAPI *MM_GET_THREAD_MANAGER)(ITfThreadMgr **);

BOOL WINAPI DllMain(HINSTANCE instance, DWORD reason, LPVOID reserved) {
    (void)reserved;
    if (reason == DLL_PROCESS_ATTACH) DisableThreadLibraryCalls(instance);
    return TRUE;
}

static BOOL mm_request_matches(const MM_REQUEST *request, HWND window,
                               ULONGLONG nonce) {
    DWORD process = 0;
    DWORD thread = GetWindowThreadProcessId(window, &process);
    ULONGLONG now = GetTickCount64();
    return request->magic == MM_REQUEST_MAGIC &&
        request->version == MM_VERSION && request->size == sizeof(MM_SHARED) &&
        request->reserved == 0 && request->helper_process != 0 &&
        request->helper_process != GetCurrentProcessId() &&
        request->process == GetCurrentProcessId() &&
        request->thread == GetCurrentThreadId() &&
        request->process == process && request->thread == thread &&
        request->window == (ULONGLONG)(UINT_PTR)window &&
        nonce != 0 && request->nonce == nonce &&
        request->deadline > now && request->deadline - now <= MM_MAX_TIMEOUT;
}

static BOOL mm_query_live(const MM_REQUEST *request, MM_SHARED *shared) {
    return GetTickCount64() < request->deadline &&
        InterlockedCompareExchange(&shared->state, MM_CLAIMED, MM_CLAIMED) == MM_CLAIMED;
}

static void mm_read_profile(MM_RESULT *result, const MM_REQUEST *request, MM_SHARED *shared) {
    HMODULE module = GetModuleHandleW(L"msctf.dll");
    MM_GET_THREAD_MANAGER get_manager;
    ITfThreadMgr *thread_manager = NULL;
    ITfInputProcessorProfileMgr *profiles = NULL;
    TF_INPUTPROCESSORPROFILE active;
    HRESULT hr;
    HKL layout;

    result->status = MM_NO_EXISTING_TSF;
    if (module == NULL) return;
    get_manager = (MM_GET_THREAD_MANAGER)(UINT_PTR)GetProcAddress(module, "TF_GetThreadMgr");
    if (get_manager == NULL) {
        result->winerr = GetLastError();
        return;
    }
    if (!mm_query_live(request, shared)) {
        result->status = MM_EXPIRED;
        return;
    }
    hr = get_manager(&thread_manager);
    result->hresult = hr;
    if (hr != S_OK || thread_manager == NULL) goto done;

    /* Never initialize COM, create/activate a thread manager, or select an IME. */
    if (!mm_query_live(request, shared)) {
        result->status = MM_EXPIRED;
        goto done;
    }
    result->status = MM_PROFILE_MANAGER_FAILED;
    hr = CoCreateInstance(&mm_profiles_clsid, NULL, CLSCTX_INPROC_SERVER,
                          &mm_profile_manager_iid, (void **)&profiles);
    result->hresult = hr;
    if (hr != S_OK || profiles == NULL) goto done;

    mm_zero(&active, sizeof(active));
    if (!mm_query_live(request, shared)) {
        result->status = MM_EXPIRED;
        goto done;
    }
    result->status = MM_ACTIVE_PROFILE_FAILED;
    hr = ITfInputProcessorProfileMgr_GetActiveProfile(profiles, &mm_keyboard_category, &active);
    result->hresult = hr;
    if (hr != S_OK) goto done;
    layout = GetKeyboardLayout(0);
    result->status = MM_INVALID_PROFILE;
    if (layout == NULL || active.langid == 0 ||
        LOWORD((UINT_PTR)layout) != active.langid) goto done;

    if (active.dwProfileType == TF_PROFILETYPE_INPUTPROCESSOR) {
        if (!mm_equal(&active.catid, &mm_keyboard_category, sizeof(GUID)) ||
            mm_equal(&active.clsid, &mm_null_guid, sizeof(GUID)) ||
            mm_equal(&active.guidProfile, &mm_null_guid, sizeof(GUID))) goto done;
        mm_copy(&result->clsid, &active.clsid, sizeof(GUID));
        mm_copy(&result->profile, &active.guidProfile, sizeof(GUID));
    } else if (active.dwProfileType == TF_PROFILETYPE_KEYBOARDLAYOUT) {
        if ((DWORD)(UINT_PTR)active.hkl != (DWORD)(UINT_PTR)layout) goto done;
    } else {
        goto done;
    }
    result->kind = active.dwProfileType;
    result->language = active.langid;
    result->layout = (ULONGLONG)(UINT_PTR)layout;
    result->status = MM_OK;

done:
    if (profiles != NULL) ITfInputProcessorProfileMgr_Release(profiles);
    if (thread_manager != NULL) ITfThreadMgr_Release(thread_manager);
}

__declspec(dllexport) LRESULT CALLBACK MouseMapperProbeHook(int code, WPARAM sent_by_thread,
                                                           LPARAM message_pointer) {
    const CWPSTRUCT *message;
    WCHAR name[80];
    ULONGLONG nonce;
    HANDLE mapping;
    MM_SHARED *shared;
    MM_REQUEST request;
    MM_RESULT result;
    UINT query_message;

    if (code != HC_ACTION || message_pointer == 0) goto next;
    message = (const CWPSTRUCT *)message_pointer;
    if (message->message < 0xc000) goto next;
    query_message = RegisterWindowMessageW(MM_MESSAGE_NAME);
    if (query_message == 0 || message->message != query_message) goto next;
    /* LPARAM may be sign-extended by a Windows cross-architecture thunk. */
    nonce = ((ULONGLONG)(DWORD)(UINT_PTR)message->lParam << 32) | (DWORD)message->wParam;
    if (nonce == 0) goto next;
    mm_mapping_name(name, nonce);
    mapping = OpenFileMappingW(FILE_MAP_READ | FILE_MAP_WRITE, FALSE, name);
    if (mapping == NULL) goto next;
    shared = (MM_SHARED *)MapViewOfFile(mapping, FILE_MAP_READ | FILE_MAP_WRITE,
                                      0, 0, sizeof(MM_SHARED));
    if (shared == NULL) {
        CloseHandle(mapping);
        goto next;
    }
    mm_copy(&request, &shared->request, sizeof(request));
    if (shared->reserved != 0 || !mm_request_matches(&request, message->hwnd, nonce) ||
        InterlockedCompareExchange(&shared->state, MM_CLAIMED, MM_PENDING) != MM_PENDING) {
        goto close_mapping;
    }

    mm_init_result(&result, &request);
    if (GetTickCount64() >= request.deadline) {
        result.status = MM_EXPIRED;
    } else {
        mm_read_profile(&result, &request, shared);
    }
    /* A cancelled/timed-out sender never receives a late successful sample. */
    if (mm_request_matches(&request, message->hwnd, nonce) &&
        mm_equal(&request, &shared->request, sizeof(request)) &&
        InterlockedCompareExchange(&shared->state, MM_CLAIMED, MM_CLAIMED) == MM_CLAIMED) {
        mm_copy(&shared->result, &result, sizeof(result));
        InterlockedCompareExchange(&shared->state, MM_COMPLETE, MM_CLAIMED);
    }

close_mapping:
    UnmapViewOfFile(shared);
    CloseHandle(mapping);
next:
    return CallNextHookEx(NULL, code, sent_by_thread, message_pointer);
}
