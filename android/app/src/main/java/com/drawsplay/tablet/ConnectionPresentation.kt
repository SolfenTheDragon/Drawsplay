package com.drawsplay.tablet

enum class UsbConnectionStatus { WAITING, PERMISSION_NEEDED, PERMISSION_DENIED, CONNECTED, OPEN_FAILED }
enum class PairedConnectionStatus { IDLE, RECONNECTING, NETWORK_UNAVAILABLE, CONNECTING, WAITING_PICTURE, TRUST_REPAIR, IDENTITY_REPAIR }
enum class ConnectionStatus {
    WAITING, STREAMING, WAITING_PICTURE, USB_PERMISSION, USB_DENIED, CONNECTING_USB,
    USB_ERROR, USB_PRIORITY, RECONNECTING_WIFI, WIFI_NETWORK, CONNECTING_WIFI, TRUST_REPAIR, IDENTITY_REPAIR
}

object ConnectionPresentation {
    fun status(
        configured: Boolean = false,
        picture: Boolean = false,
        savedWifi: Boolean = false,
        usb: UsbConnectionStatus = UsbConnectionStatus.WAITING,
        wifi: PairedConnectionStatus = PairedConnectionStatus.IDLE,
        usbBlocksWifi: Boolean = false
    ): ConnectionStatus = when {
        picture -> ConnectionStatus.STREAMING
        configured || wifi == PairedConnectionStatus.WAITING_PICTURE -> ConnectionStatus.WAITING_PICTURE
        usb == UsbConnectionStatus.PERMISSION_NEEDED -> ConnectionStatus.USB_PERMISSION
        usb == UsbConnectionStatus.PERMISSION_DENIED -> ConnectionStatus.USB_DENIED
        usb == UsbConnectionStatus.OPEN_FAILED -> ConnectionStatus.USB_ERROR
        usb == UsbConnectionStatus.CONNECTED -> ConnectionStatus.CONNECTING_USB
        savedWifi && usbBlocksWifi -> ConnectionStatus.USB_PRIORITY
        wifi == PairedConnectionStatus.TRUST_REPAIR -> ConnectionStatus.TRUST_REPAIR
        wifi == PairedConnectionStatus.IDENTITY_REPAIR -> ConnectionStatus.IDENTITY_REPAIR
        wifi == PairedConnectionStatus.NETWORK_UNAVAILABLE -> ConnectionStatus.WIFI_NETWORK
        wifi == PairedConnectionStatus.CONNECTING -> ConnectionStatus.CONNECTING_WIFI
        savedWifi -> ConnectionStatus.RECONNECTING_WIFI
        else -> ConnectionStatus.WAITING
    }
}

enum class PairingStatus(val canRetry: Boolean = false) {
    SEARCHING, ENTER_CODE, CHECKING, APPROVAL, SAVING, PAIRED, INVALID_CODE,
    EXPIRED(true), NOT_APPROVED(true), VERIFICATION_FAILED(true), NETWORK_UNAVAILABLE(true),
    DISCOVERY_FAILED(true), TRUST_REPAIR(true), IDENTITY_REPAIR(true), FAILED(true), COMPUTER_NOT_FOUND(true)
}

/** UI-owned generation prevents cancelled attempts updating a replacement panel. */
class PairingAttemptState {
    private var generation = 0L
    private var active = false
    var status = PairingStatus.SEARCHING
        private set

    fun start(): Long {
        active = true
        status = PairingStatus.SEARCHING
        return ++generation
    }

    fun update(attempt: Long, next: PairingStatus): Boolean {
        if (!active || generation != attempt) return false
        if ((status.canRetry || status == PairingStatus.PAIRED) && next != status) return false
        status = next
        return true
    }

    fun cancel() {
        active = false
        generation++
    }
}
