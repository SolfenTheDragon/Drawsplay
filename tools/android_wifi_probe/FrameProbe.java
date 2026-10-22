package com.drawsplay.wifiprobe;

import com.drawsplay.tablet.transport.TlsSocketFactory;
import com.drawsplay.tablet.transport.WiFiFrameCodec;
import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.util.Arrays;

/** Isolated synthetic framing experiment; no display or input-injection API. */
public final class FrameProbe {
    private static final String FIXTURE =
        "0300002400000003010203040506070800000009000000000000000a0000000000000000616263";

    public static void main(String[] args) throws Exception {
        selfTest();
        if (args.length == 1 && args[0].equals("self-test")) return;
        if (args.length != 3 || args[0].equals(args[1])) {
            throw new IllegalArgumentException("Require selected tablet IPv4, on-link host IPv4 and test pin");
        }
        InetAddress tablet = numericIPv4(args[0]);
        InetAddress host = numericIPv4(args[1]);
        try (ServerSocket listener = new ServerSocket(0, 1, tablet)) {
            listener.setSoTimeout(30000);
            System.out.println("{\"readyTabletPort\":" + listener.getLocalPort() + "}");
            Socket incoming = listener.accept();
            if (!incoming.getInetAddress().equals(host)) {
                incoming.close();
                throw new IllegalArgumentException("Unexpected probe peer");
            }
            try (Socket socket = TlsSocketFactory.INSTANCE.authenticateAccepted(incoming, 5000, args[2])) {
                socket.setSoTimeout(5000);
                WiFiFrameCodec.Reader reader = new WiFiFrameCodec.Reader(socket.getInputStream());
                WiFiFrameCodec.Writer writer = new WiFiFrameCodec.Writer(socket.getOutputStream());
                byte[] payload = new byte[WiFiFrameCodec.MAX_ACCESS_UNIT];
                WiFiFrameCodec.SequenceGuard sequence = null;
                long bytes = 0;
                for (int frame = 1; frame <= 9; frame++) {
                    WiFiFrameCodec.Header header = reader.readHeader();
                    if (header.getType() != WiFiFrameCodec.VIDEO_AU || header.getSequence() != frame) {
                        throw new AssertionError("Unexpected synthetic AU envelope");
                    }
                    if (sequence == null) sequence = new WiFiFrameCodec.SequenceGuard(header.getEpoch());
                    sequence.accept(header);
                    int length = reader.readPayload(payload);
                    for (int index = 0; index < length; index++) {
                        if (payload[index] != (byte) (index * 31 + frame)) {
                            throw new AssertionError("Synthetic AU payload mismatch");
                        }
                    }
                    writer.writeFrame(header, payload, 0, length);
                    bytes += length;
                }
                System.out.println("{\"verifiedFrames\":9,\"verifiedPayloadBytesEachDirection\":" + bytes
                    + ",\"payloadBuffers\":1,\"payloadBufferBytes\":" + payload.length
                    + ",\"tcpInitiator\":\"host\",\"tlsServer\":\"host\"}");
            }
        }
    }

    private static InetAddress numericIPv4(String value) throws Exception {
        if (!value.matches("[0-9]{1,3}(\\.[0-9]{1,3}){3}")) {
            throw new IllegalArgumentException("Require numeric IPv4; no DNS lookup");
        }
        String[] parts = value.split("\\.");
        byte[] bytes = new byte[4];
        for (int index = 0; index < 4; index++) {
            int octet = Integer.parseInt(parts[index]);
            if (octet > 255) throw new IllegalArgumentException("Invalid IPv4 octet");
            bytes[index] = (byte) octet;
        }
        return InetAddress.getByAddress(bytes);
    }

    private static void selfTest() throws Exception {
        byte[] expected = new byte[FIXTURE.length() / 2];
        for (int index = 0; index < expected.length; index++) {
            expected[index] = (byte) Integer.parseInt(FIXTURE.substring(index * 2, index * 2 + 2), 16);
        }
        ByteArrayInputStream input = new ByteArrayInputStream(expected);
        WiFiFrameCodec.Reader reader = new WiFiFrameCodec.Reader(input);
        WiFiFrameCodec.Header header = reader.readHeader();
        if (header.getType() != WiFiFrameCodec.INPUT || header.getEpoch() != 0x0102030405060708L
                || header.getSequence() != 9 || input.available() != 3) {
            throw new AssertionError("Big-endian fixture mismatch or eager payload read");
        }
        byte[] payload = new byte[3];
        reader.readPayload(payload);
        ByteArrayOutputStream output = new ByteArrayOutputStream();
        new WiFiFrameCodec.Writer(output).writeFrame(header, payload, 0, payload.length);
        if (!Arrays.equals(expected, output.toByteArray())) throw new AssertionError("Wire fixture mismatch");
        System.out.println("{\"frameCodecSelfTest\":true}");
    }
}
