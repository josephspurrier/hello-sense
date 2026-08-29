//
//  HEMSupportViewController.m
//  Sense
//
//  Created by Jimmy Lu on 6/4/15.
//  Copyright (c) 2015 Hello. All rights reserved.
//
#import "Sense-Swift.h"

#import "HEMSupportViewController.h"
#import "HEMSettingsStoryboard.h"
#import "HEMSupportUtil.h"
#import "HEMActivityCoverView.h"
#import "HEMScreenUtils.h"
#import "HEMSettingsHeaderFooterView.h"

// The "My Tickets" row was a Zendesk ticket list and has no equivalent now that
// support runs over email, so only the first two rows remain.
typedef NS_ENUM(NSUInteger, HEMSupportRow) {
    HEMSupportRowIndexUserGuide = 0,
    HEMSupportRowIndexContactUs = 1,
    HEMSupportRows = 2
};

@interface HEMSupportViewController() <UITableViewDataSource, UITableViewDelegate>

@property (weak, nonatomic) IBOutlet UITableView *tableView;

@end

@implementation HEMSupportViewController

- (void)viewDidLoad {
    [super viewDidLoad];
    [self configureTableView];

    [SENAnalytics track:HEMAnalyticsEventSupport];
}

- (void)configureTableView {
    UIView* header = [[HEMSettingsHeaderFooterView alloc] initWithTopBorder:NO bottomBorder:NO];
    UIView* footer = [[HEMSettingsHeaderFooterView alloc] initWithTopBorder:NO bottomBorder:NO];
    [[self tableView] setTableHeaderView:header];
    [[self tableView] setTableFooterView:footer];
    [[self tableView] applyStyle];
}

#pragma mark - UITableViewDataSource / Delegate

- (NSInteger)tableView:(UITableView *)tableView numberOfRowsInSection:(NSInteger)section {
    return HEMSupportRows;
}

- (UITableViewCell*)tableView:(UITableView *)tableView
        cellForRowAtIndexPath:(NSIndexPath *)indexPath {
    NSString* reuseId = [HEMSettingsStoryboard supportCellReuseIdentifier];
    return [tableView dequeueReusableCellWithIdentifier:reuseId];
}

- (NSString*)titleForRowAtIndexPath:(NSIndexPath*)indexPath {
    switch ([indexPath row]) {
        case HEMSupportRowIndexUserGuide:
            return NSLocalizedString(@"settings.user-guide", nil);
        case HEMSupportRowIndexContactUs:
            return NSLocalizedString(@"settings.contact-us", nil);
        default:
            return nil;
    }
}

- (void)tableView:(UITableView *)tableView
  willDisplayCell:(UITableViewCell *)cell
forRowAtIndexPath:(NSIndexPath *)indexPath {
    [[cell textLabel] setText:[self titleForRowAtIndexPath:indexPath]];
    [cell showStyledAccessoryViewIfNone];
    [cell applyDetailAccessoryStyle];
    [cell applyStyle];
    [cell showStyledSelectionView];
}

- (void)tableView:(UITableView *)tableView didSelectRowAtIndexPath:(NSIndexPath *)indexPath {
    [tableView deselectRowAtIndexPath:indexPath animated:YES];
    
    switch ([indexPath row]) {
        case HEMSupportRowIndexUserGuide: {
            [SENAnalytics track:HEMAnalyticsEventSupportUserGuide];
            // The Zendesk help centre is replaced by the support site, opened
            // through the same helper the rest of the app already uses.
            [HEMSupportUtil openHelpFrom:self];
            break;
        }
        case HEMSupportRowIndexContactUs:
            [SENAnalytics track:HEMAnalyticsEventSupportContactUs];
            [self performSegueWithIdentifier:[HEMSettingsStoryboard topicsSegueIdentifier] sender:self];
            break;
        default:
            break;
    }
    
}

- (void)scrollViewDidScroll:(UIScrollView *)scrollView {
    [[self shadowView] updateVisibilityWithContentOffset:[scrollView contentOffset].y];
}

#pragma mark - Clean up

- (void)dealloc {
    [_tableView setDelegate:nil];
    [_tableView setDataSource:nil];
}

@end
