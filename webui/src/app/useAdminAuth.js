import { useCallback, useEffect, useRef, useState } from 'react'

export function useAdminAuth({ isProduction, location, t }) {
    const [message, setMessage] = useState(null)
    const [token, setToken] = useState(null)
    const [authChecking, setAuthChecking] = useState(true)
    const authExpiredNotifiedRef = useRef(false)

    const isAdminRoute = location.pathname.startsWith('/admin') || isProduction

    const showMessage = useCallback((type, text) => {
        setMessage({ type, text })
        setTimeout(() => setMessage(null), 5000)
    }, [])

    // Do not persist admin JWTs in Web Storage (localStorage/sessionStorage),
    // because they are cleartext and accessible to any script running in the
    // page context. Keep auth in memory only; this function only cleans up
    // legacy keys from older versions.
    const clearStoredCredentials = () => {
        localStorage.removeItem('deepseek-web-to-api_token')
        localStorage.removeItem('deepseek-web-to-api_token_expires')
        sessionStorage.removeItem('deepseek-web-to-api_token')
        sessionStorage.removeItem('deepseek-web-to-api_token_expires')
    }

    const handleLogout = useCallback(() => {
        authExpiredNotifiedRef.current = false
        setToken(null)
        clearStoredCredentials()
    }, [])

    const handleLogin = useCallback((newToken) => {
        authExpiredNotifiedRef.current = false
        setToken(newToken)
    }, [])

    const handleAuthExpired = useCallback(() => {
        setToken(null)
        clearStoredCredentials()
        if (!authExpiredNotifiedRef.current) {
            authExpiredNotifiedRef.current = true
            showMessage('error', t('auth.expired'))
        }
    }, [showMessage, t])

    useEffect(() => {
        if (!isAdminRoute) {
            setAuthChecking(false)
            return
        }

        const checkAuth = async () => {
            // Security: avoid reading/writing bearer tokens from/to Web Storage.
            // Admin auth is in-memory only; require explicit login after refresh.
            const storedToken = null
            const expiresAt = 0

            if (storedToken && expiresAt > Date.now()) {
                try {
                    const res = await fetch('/admin/verify', {
                        headers: { 'Authorization': `Bearer ${storedToken}` }
                    })
                    if (res.ok) {
                        setToken(storedToken)
                    } else if (res.status === 401 || res.status === 403) {
                        // Token explicitly rejected — server told us it is
                        // invalid (admin password change bumped JWTValidAfterUnix,
                        // or token was issued before a clear-tokens action).
                        handleAuthExpired()
                    } else {
                        // 5xx / unexpected status — server is up but unhappy;
                        // do not silently keep the token alive. Treat as a
                        // soft-fail and require explicit login.
                        handleAuthExpired()
                    }
                } catch {
                    // Network failure (server down / offline / DNS error).
                    // The previous behaviour of trusting the cached token here
                    // was a security issue: an attacker who controls the
                    // network could induce a permanent skip of server-side
                    // token revocation. Treat as auth failure; the user can
                    // retry once connectivity is back.
                    handleAuthExpired()
                }
            }
            setAuthChecking(false)
        }

        checkAuth()
    }, [handleAuthExpired, isAdminRoute])

    return {
        token,
        authChecking,
        message,
        isAdminRoute,
        showMessage,
        handleLogin,
        handleLogout,
        handleAuthExpired,
    }
}
