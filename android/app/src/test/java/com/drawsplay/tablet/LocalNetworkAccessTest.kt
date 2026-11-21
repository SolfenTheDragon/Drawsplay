package com.drawsplay.tablet

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class LocalNetworkAccessTest {
    @Test
    fun enforcedOnAndroid17ForAppsTargetingApi37() {
        assertTrue(LocalNetworkAccess.enforced(deviceSdk = 37, targetSdk = 37))
        assertTrue(LocalNetworkAccess.enforced(deviceSdk = 38, targetSdk = 37))
    }

    @Test
    fun olderDevicesNeverEnforce() {
        assertFalse(LocalNetworkAccess.enforced(deviceSdk = 36, targetSdk = 37))
        assertFalse(LocalNetworkAccess.enforced(deviceSdk = 26, targetSdk = 37))
    }

    @Test
    fun legacyTargetsKeepImplicitAccess() {
        assertFalse(LocalNetworkAccess.enforced(deviceSdk = 37, targetSdk = 36))
    }
}
