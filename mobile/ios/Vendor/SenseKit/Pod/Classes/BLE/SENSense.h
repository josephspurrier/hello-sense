//
//  SENSense.h
//  Pods
//
//  Created by Jimmy Lu on 8/22/14.
//  Copyright (c) 2014 Hello Inc. All rights reserved.
//

#import <Foundation/Foundation.h>

@class LGPeripheral;

typedef NS_ENUM(NSUInteger, SENSenseMode) {
    SENSenseModeUnknown = 0,
    SENSenseModeNormal = 1,
    SENSenseModePairing = 2
};

typedef NS_ENUM(NSUInteger, SENSenseAdvertisedVersion) {
    SENSenseAdvertisedVersionUnknown = 0,
    SENSenseAdvertisedVersionVoice
};

@interface SENSense : NSObject

@property (nonatomic, copy, readonly) NSString* name;
@property (nonatomic, strong, readonly) NSString* macAddress;
@property (nonatomic, copy, readonly) NSString* deviceId;
@property (nonatomic, assign, readonly) SENSenseMode mode;
@property (nonatomic, strong, readonly) LGPeripheral* peripheral;
@property (nonatomic, assign, readonly) SENSenseAdvertisedVersion version;

- (instancetype)initWithPeripheral:(LGPeripheral*)peripheral;
- (instancetype)initWithPeripheral:(LGPeripheral*)peripheral andDeviceId:(NSString*)deviceId;

/**
 * Whether this scanned Sense is the one identified by deviceId.
 *
 * Prefers the device id carried in the advertised service data. That data can
 * be absent: Sense splits its advertisement across the advertising packet and
 * the scan response, and iOS does not always fetch the scan response, leaving
 * only the local name to go on. In that case the advertised name is matched
 * instead, which encodes the leading byte of the device id.
 */
- (BOOL)matchesDeviceId:(NSString*)deviceId;

/**
 * Record the device id of a Sense that was matched by name.
 *
 * A Sense identified without its advertised service data has no device id of
 * its own, and the rest of SenseKit needs one: -saveSenseUUID skips the write
 * without it, which would send every later visit back through a scan instead of
 * going straight to the known peripheral.
 */
- (void)adoptDeviceId:(NSString*)deviceId;

@end
